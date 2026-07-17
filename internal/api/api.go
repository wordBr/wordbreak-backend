// Package api wires the game + referee into an HTTP surface for the MiniPay frontend.
//
// Routes:
//
//	GET  /health
//	GET  /api/referee                      -> referee address (to configure the contract)
//	GET  /api/solo/rack?size=6             -> a fresh solo rack (answers withheld)
//	POST /api/solo/score                   -> score {letters, words[]}
//	GET  /api/daily                        -> today's shared rack
//	POST /api/daily/submit                 -> submit {address, words[]} for today
//	GET  /api/daily/leaderboard?date=...    -> ranked standings
//	POST /api/admin/sign-settlement        -> referee signs {roundId, winners[], amounts[]}
//
// Admin routes require the X-Admin-Token header. Signing requires a configured referee key.
package api

import (
	"encoding/json"
	"math/big"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/wordbreak/backend/internal/chain"
	"github.com/wordbreak/backend/internal/dictionary"
	"github.com/wordbreak/backend/internal/game"
	"github.com/wordbreak/backend/internal/rack"
	"github.com/wordbreak/backend/internal/room"
	"github.com/wordbreak/backend/internal/signer"
	"github.com/wordbreak/backend/internal/store"
)

// Config holds runtime knobs.
type Config struct {
	SoloRackSize  int
	DailyRackSize int
	AdminToken    string // if empty, admin routes are disabled
}

// Server is the API dependencies.
type Server struct {
	dict   *dictionary.Dictionary
	store  *store.Store
	signer *signer.Signer // may be nil if no referee key configured
	chain  *chain.Client  // may be nil if no RPC configured
	rooms  *room.Manager
	cfg    Config
}

// New builds a Server. signer and chainCli may be nil (game works; signing/paid daily 503).
func New(d *dictionary.Dictionary, st *store.Store, sg *signer.Signer, chainCli *chain.Client, cfg Config) *Server {
	if cfg.SoloRackSize == 0 {
		cfg.SoloRackSize = 6
	}
	if cfg.DailyRackSize == 0 {
		cfg.DailyRackSize = 6
	}
	return &Server{dict: d, store: st, signer: sg, chain: chainCli, rooms: room.NewManager(d), cfg: cfg}
}

// EnableRoomStaking wires the on-chain writer into the multiplayer room manager, turning on
// staked rooms. Without calling this, Create rejects any non-zero stake.
func (s *Server) EnableRoomStaking(w *chain.Writer, chainCli *chain.Client, sg *signer.Signer) {
	s.rooms.EnableStaking(w, chainCli, sg)
}

// Routes returns the HTTP handler (Go 1.22+ method+path patterns).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/referee", s.handleReferee)
	mux.HandleFunc("GET /api/solo/rack", s.handleSoloRack)
	mux.HandleFunc("POST /api/solo/score", s.handleSoloScore)
	mux.HandleFunc("GET /api/daily", s.handleDaily)
	mux.HandleFunc("POST /api/daily/submit", s.handleDailySubmit)
	mux.HandleFunc("GET /api/daily/leaderboard", s.handleLeaderboard)
	mux.HandleFunc("POST /api/admin/daily/open", s.handleOpenDaily)
	mux.HandleFunc("POST /api/admin/sign-settlement", s.handleSignSettlement)
	// multiplayer rooms
	mux.HandleFunc("POST /api/room/create", s.handleRoomCreate)
	mux.HandleFunc("POST /api/room/join", s.handleRoomJoin)
	mux.HandleFunc("POST /api/room/start", s.handleRoomStart)
	mux.HandleFunc("POST /api/room/submit", s.handleRoomSubmit)
	mux.HandleFunc("GET /api/room/list", s.handleRoomList)
	mux.HandleFunc("GET /api/room/{code}", s.handleRoomGet)
	return withCORS(mux)
}

// --- handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "words": s.dict.Size()})
}

func (s *Server) handleReferee(w http.ResponseWriter, _ *http.Request) {
	if s.signer == nil {
		writeErr(w, http.StatusServiceUnavailable, "referee signing not configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"referee": s.signer.Address().Hex()})
}

func (s *Server) handleSoloRack(w http.ResponseWriter, r *http.Request) {
	size := clampSize(intQuery(r, "size", s.cfg.SoloRackSize))
	res := rack.GenerateSolo(s.dict, size)
	// Solo is free practice, so we return the answer set for instant, offline-friendly
	// validation. The PAID daily deliberately never does this (see handleDaily).
	writeJSON(w, http.StatusOK, map[string]any{
		"letters":   res.Letters,
		"wordCount": len(res.Words),
		"words":     res.Words,
	})
}

func (s *Server) handleSoloScore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Letters string   `json:"letters"`
		Words   []string `json:"words"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Letters == "" {
		writeErr(w, http.StatusBadRequest, "letters required")
		return
	}
	writeJSON(w, http.StatusOK, game.Score(s.dict, req.Letters, req.Words))
}

func (s *Server) handleDaily(w http.ResponseWriter, _ *http.Request) {
	dateKey := todayKey()
	d := s.store.GetOrCreateDaily(dateKey, func() string {
		return rack.GenerateDaily(s.dict, dateKey, s.cfg.DailyRackSize).Letters
	})
	resp := map[string]any{"dateKey": d.DateKey, "letters": d.Letters, "paid": d.Paid}
	if d.Paid {
		resp["roundId"] = d.RoundID
		resp["endTime"] = d.EndTime.Unix()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDailySubmit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string   `json:"address"`
		Words   []string `json:"words"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !common.IsHexAddress(req.Address) {
		writeErr(w, http.StatusBadRequest, "valid address required")
		return
	}

	dateKey := todayKey()
	d := s.store.GetOrCreateDaily(dateKey, func() string {
		return rack.GenerateDaily(s.dict, dateKey, s.cfg.DailyRackSize).Letters
	})

	// Fund-safety gate: for a paid round, only score addresses that actually paid in, and
	// only while entry is still open. Without this, an unpaid address could be scored,
	// land on the leaderboard, and be signed as a winner — draining the honest pot.
	if d.Paid {
		if time.Now().UTC().After(d.EndTime) {
			writeErr(w, http.StatusForbidden, "today's pool has closed")
			return
		}
		if s.chain == nil {
			writeErr(w, http.StatusServiceUnavailable, "pool verification unavailable")
			return
		}
		roundID, ok := new(big.Int).SetString(d.RoundID, 10)
		if !ok {
			writeErr(w, http.StatusInternalServerError, "bad round id")
			return
		}
		entered, err := s.chain.HasEntered(r.Context(), roundID, common.HexToAddress(req.Address))
		if err != nil {
			writeErr(w, http.StatusBadGateway, "could not verify pool entry")
			return
		}
		if !entered {
			writeErr(w, http.StatusForbidden, "enter today's pool before submitting")
			return
		}
	}

	// Score against the SERVER's letters — never trust client-supplied letters for the paid round.
	result := game.Score(s.dict, d.Letters, req.Words)
	d.Submit(store.Submission{
		Address: req.Address,
		Score:   result.Total,
		Words:   len(result.Accepted),
		At:      time.Now().UTC(),
	})

	rank := 0
	for _, e := range d.Leaderboard() {
		if common.HexToAddress(e.Address) == common.HexToAddress(req.Address) {
			rank = e.Rank
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dateKey": dateKey,
		"score":   result.Total,
		"rank":    rank,
		"result":  result,
	})
}

func (s *Server) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	dateKey := r.URL.Query().Get("date")
	if dateKey == "" {
		dateKey = todayKey()
	}
	d := s.store.GetOrCreateDaily(dateKey, func() string {
		return rack.GenerateDaily(s.dict, dateKey, s.cfg.DailyRackSize).Letters
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"dateKey":     d.DateKey,
		"leaderboard": d.Leaderboard(),
	})
}

// handleOpenDaily registers today's on-chain round with the backend so paid submissions can
// be gated. The operator calls this right after creating the round on-chain, passing the same
// roundId and the round's endTime.
func (s *Server) handleOpenDaily(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(w, r) {
		return
	}
	var req struct {
		RoundID string `json:"roundId"`
		EndTime int64  `json:"endTime"` // unix seconds
		DateKey string `json:"dateKey"` // optional; defaults to today (UTC)
	}
	if !decode(w, r, &req) {
		return
	}
	if _, ok := new(big.Int).SetString(req.RoundID, 10); !ok {
		writeErr(w, http.StatusBadRequest, "roundId must be a base-10 integer string")
		return
	}
	if req.EndTime <= time.Now().UTC().Unix() {
		writeErr(w, http.StatusBadRequest, "endTime must be in the future")
		return
	}
	dateKey := req.DateKey
	if dateKey == "" {
		dateKey = todayKey()
	}
	letters := rack.GenerateDaily(s.dict, dateKey, s.cfg.DailyRackSize).Letters
	d := s.store.OpenPaidDaily(dateKey, req.RoundID, time.Unix(req.EndTime, 0).UTC(), letters)
	writeJSON(w, http.StatusOK, map[string]any{
		"dateKey": d.DateKey,
		"roundId": d.RoundID,
		"letters": d.Letters,
		"endTime": d.EndTime.Unix(),
		"paid":    true,
	})
}

func (s *Server) handleSignSettlement(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(w, r) {
		return
	}
	if s.signer == nil {
		writeErr(w, http.StatusServiceUnavailable, "referee signing not configured")
		return
	}
	var req struct {
		RoundID string   `json:"roundId"`
		Winners []string `json:"winners"`
		Amounts []string `json:"amounts"`
	}
	if !decode(w, r, &req) {
		return
	}
	roundID, ok := new(big.Int).SetString(req.RoundID, 10)
	if !ok {
		writeErr(w, http.StatusBadRequest, "roundId must be a base-10 integer string")
		return
	}
	if len(req.Winners) != len(req.Amounts) || len(req.Winners) == 0 {
		writeErr(w, http.StatusBadRequest, "winners and amounts must be non-empty and equal length")
		return
	}
	winners := make([]common.Address, len(req.Winners))
	amounts := make([]*big.Int, len(req.Amounts))
	for i := range req.Winners {
		if !common.IsHexAddress(req.Winners[i]) {
			writeErr(w, http.StatusBadRequest, "invalid winner address: "+req.Winners[i])
			return
		}
		amt, ok := new(big.Int).SetString(req.Amounts[i], 10)
		if !ok {
			writeErr(w, http.StatusBadRequest, "invalid amount: "+req.Amounts[i])
			return
		}
		winners[i] = common.HexToAddress(req.Winners[i])
		amounts[i] = amt
	}

	// Defense in depth: never sign a payout to an address that didn't enter this round.
	// The submit gate already keeps non-entrants off the leaderboard; this stops an operator
	// mistake (or a compromised admin call) from paying someone who never paid in.
	if s.chain != nil {
		for _, wnr := range winners {
			entered, err := s.chain.HasEntered(r.Context(), roundID, wnr)
			if err != nil {
				writeErr(w, http.StatusBadGateway, "could not verify winner entry: "+wnr.Hex())
				return
			}
			if !entered {
				writeErr(w, http.StatusBadRequest, "winner never entered round: "+wnr.Hex())
				return
			}
		}
	}

	sig, err := s.signer.SignSettlement(roundID, winners, amounts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"roundId":   req.RoundID,
		"referee":   s.signer.Address().Hex(),
		"signature": "0x" + common.Bytes2Hex(sig),
	})
}

// --- multiplayer rooms ---

func (s *Server) handleRoomCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PlayerId string `json:"playerId"`
		Name     string `json:"name"`
		Public   bool   `json:"public"`
		Stake    string `json:"stake"` // decimal wei string; "" or "0" = free
	}
	if !decode(w, r, &req) {
		return
	}
	if req.PlayerId == "" {
		writeErr(w, http.StatusBadRequest, "playerId required")
		return
	}
	opts := room.CreateOpts{Public: req.Public}
	if req.Stake != "" && req.Stake != "0" {
		amt, ok := new(big.Int).SetString(req.Stake, 10)
		if !ok || amt.Sign() <= 0 {
			writeErr(w, http.StatusBadRequest, "invalid stake amount")
			return
		}
		opts.Stake = amt
	}
	view, err := s.rooms.Create(req.PlayerId, req.Name, opts)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleRoomList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"rooms": s.rooms.List()})
}

func (s *Server) handleRoomJoin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		PlayerId string `json:"playerId"`
		Name     string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.PlayerId == "" || req.Code == "" {
		writeErr(w, http.StatusBadRequest, "code and playerId required")
		return
	}
	view, err := s.rooms.Join(req.Code, req.PlayerId, req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleRoomStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		PlayerId string `json:"playerId"`
	}
	if !decode(w, r, &req) {
		return
	}
	view, err := s.rooms.Start(req.Code, req.PlayerId)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleRoomSubmit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		PlayerId string `json:"playerId"`
		Word     string `json:"word"`
	}
	if !decode(w, r, &req) {
		return
	}
	accepted, pts, view, err := s.rooms.Submit(req.Code, req.PlayerId, req.Word)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": accepted, "points": pts, "room": view})
}

func (s *Server) handleRoomGet(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	view, ok := s.rooms.Get(code, r.URL.Query().Get("you"))
	if !ok {
		writeErr(w, http.StatusNotFound, "room not found")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// --- helpers ---

func (s *Server) adminOK(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.AdminToken == "" {
		writeErr(w, http.StatusServiceUnavailable, "admin routes disabled (no ADMIN_TOKEN)")
		return false
	}
	if r.Header.Get("X-Admin-Token") != s.cfg.AdminToken {
		writeErr(w, http.StatusUnauthorized, "invalid admin token")
		return false
	}
	return true
}

func todayKey() string { return time.Now().UTC().Format("2006-01-02") }

func clampSize(n int) int {
	if n < 4 {
		return 4
	}
	if n > 8 {
		return 8
	}
	return n
}

func intQuery(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Admin-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

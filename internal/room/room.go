// Package room is the multiplayer engine: short-lived, in-memory rooms where up to 5 players
// race the same rack against a shared clock. It's poll-based (clients GET a snapshot ~every
// 1.5s) — no WebSockets — which keeps it simple, reuses the HTTP + scoring layer, and is
// verifiable with a plain script. Scoring is server-authoritative (reuses game.Score), so a
// client can never report its own score. Free-room identity is just a player-id string (wallet
// address when present, a generated id otherwise) — no chain/wallet needed to play for free.
//
// Staked rooms layer real money on top, reusing the already-verified WordBreakPools contract
// (the same one the daily pool uses) instead of a new contract: the backend opens a round
// on-chain at room creation, players must independently approve+enter with their own wallet
// before they're allowed to join the room (verified via on-chain hasEntered — never trusted
// from the client), and when the race ends the backend signs and broadcasts a "winner takes
// all" settlement automatically. Staked rooms therefore require a wallet; free rooms don't.
package room

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/wordbreak/backend/internal/chain"
	"github.com/wordbreak/backend/internal/dictionary"
	"github.com/wordbreak/backend/internal/game"
	"github.com/wordbreak/backend/internal/rack"
	"github.com/wordbreak/backend/internal/signer"
)

const (
	MaxPlayers  = 5
	RaceSeconds = 60
	rackSize    = 6
	codeLen     = 4

	// How long a staked room's on-chain round stays open for enter() calls. Players must
	// stake within this window; settlement can't be broadcast until it has passed (the
	// contract enforces entry-closed-before-settle), so a race that finishes sooner has its
	// payout scheduled for the moment this window closes rather than paid immediately.
	stakeWindow = 3 * time.Minute
)

// unambiguous code alphabet (no O/0/I/1).
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

type player struct {
	id    string
	name  string
	score int
	words int
	found map[string]bool
}

type Room struct {
	mu          sync.Mutex
	code        string
	host        string
	public      bool
	state       string // "lobby" | "racing" | "done"
	letters     string
	endsAt      time.Time
	players     map[string]*player
	order       []string // join order
	dict        *dictionary.Dictionary
	raceSeconds int

	// staking (nil Stake = free room)
	stake        *big.Int
	roundID      *big.Int
	stakeEndsAt  time.Time
	settleStatus string // "" | "pending" | "settled" | "failed"
	settleErr    string
}

// Manager owns all live rooms.
type Manager struct {
	mu          sync.Mutex
	rooms       map[string]*Room
	dict        *dictionary.Dictionary
	raceSeconds int

	// optional: nil disables staking (Create rejects stake>0 with a clear error)
	writer *chain.Writer
	reader *chain.Client
	signer *signer.Signer
}

func NewManager(d *dictionary.Dictionary) *Manager {
	secs := RaceSeconds
	if v := os.Getenv("RACE_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			secs = n
		}
	}
	return &Manager{
		rooms:       make(map[string]*Room),
		dict:        d,
		raceSeconds: secs,
	}
}

// EnableStaking wires the on-chain pieces needed for staked rooms. Without this call, Create
// rejects any non-zero stake — a staked room is never created without real chain backing.
func (m *Manager) EnableStaking(w *chain.Writer, r *chain.Client, s *signer.Signer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writer = w
	m.reader = r
	m.signer = s
}

func (m *Manager) stakingEnabled() bool {
	return m.writer != nil && m.reader != nil && m.signer != nil
}

// --- views (JSON-safe snapshots) ---

type PlayerView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Score int    `json:"score"`
	Words int    `json:"words"`
}

type RoomView struct {
	Code     string       `json:"code"`
	State    string       `json:"state"`
	Host     string       `json:"host"`
	Public   bool         `json:"public"`
	Letters  string       `json:"letters"` // empty until the race starts
	TimeLeft int          `json:"timeLeft"`
	Players  []PlayerView `json:"players"` // sorted best-first
	Winner   string       `json:"winner"`  // player id, set when done
	You      string       `json:"you,omitempty"`

	// staking (zero values on free rooms)
	Stake        string `json:"stake"` // wei, decimal string; "0" = free
	Pot          string `json:"pot"`   // stake * len(players)
	RoundID      string `json:"roundId,omitempty"`
	StakeEndsIn  int    `json:"stakeEndsIn,omitempty"` // seconds until the on-chain entry window closes
	SettleStatus string `json:"settleStatus,omitempty"`
	SettleErr    string `json:"settleErr,omitempty"`
}

// --- manager ops ---

// CreateOpts configures a new room. Stake is wei (nil or zero = free).
type CreateOpts struct {
	Public bool
	Stake  *big.Int
}

func (m *Manager) Create(playerID, name string, opts CreateOpts) (*RoomView, error) {
	staked := opts.Stake != nil && opts.Stake.Sign() > 0
	if staked && !common.IsHexAddress(playerID) {
		return nil, errRoom("staked rooms need a connected wallet")
	}
	if staked && !m.stakingEnabled() {
		return nil, errRoom("staking isn't available on this server right now")
	}

	m.mu.Lock()
	code := m.freshCode()
	r := &Room{
		code:        code,
		host:        playerID,
		public:      opts.Public,
		state:       "lobby",
		players:     make(map[string]*player),
		dict:        m.dict,
		raceSeconds: m.raceSeconds,
	}
	if staked {
		r.stake = new(big.Int).Set(opts.Stake)
	}
	m.rooms[code] = r
	writer, reader := m.writer, m.reader
	m.mu.Unlock()

	if staked {
		roundID, err := freshRoundID(reader)
		if err != nil {
			m.drop(code)
			return nil, errRoom("could not prepare the on-chain round: " + err.Error())
		}
		endsAt := time.Now().Add(stakeWindow)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := writer.CreateRound(ctx, roundID, opts.Stake, uint64(endsAt.Unix())); err != nil {
			m.drop(code)
			return nil, errRoom("could not open the stake on-chain: " + err.Error())
		}
		r.mu.Lock()
		r.roundID = roundID
		r.stakeEndsAt = endsAt
		r.mu.Unlock()
	}

	r.addPlayer(playerID, name)
	return r.snapshot(playerID), nil
}

func (m *Manager) drop(code string) {
	m.mu.Lock()
	delete(m.rooms, code)
	m.mu.Unlock()
}

func (m *Manager) Get(code, viewer string) (*RoomView, bool) {
	m.mu.Lock()
	r, ok := m.rooms[strings.ToUpper(code)]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	return r.snapshot(viewer), true
}

// List returns open public rooms (lobby state, not full) — the "join anyone" browser.
func (m *Manager) List() []*RoomView {
	m.mu.Lock()
	rooms := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.mu.Unlock()

	out := make([]*RoomView, 0, len(rooms))
	for _, r := range rooms {
		r.mu.Lock()
		if r.state == "lobby" && r.public && len(r.players) < MaxPlayers {
			out = append(out, r.snapshotLocked(""))
		}
		r.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

func (m *Manager) room(code string) (*Room, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[strings.ToUpper(code)]
	return r, ok
}

func (m *Manager) freshCode() string {
	for {
		c := randCode()
		if _, exists := m.rooms[c]; !exists {
			return c
		}
	}
}

func randCode() string {
	buf := make([]byte, codeLen)
	_, _ = rand.Read(buf)
	b := make([]byte, codeLen)
	for i := range b {
		b[i] = codeAlphabet[int(buf[i])%len(codeAlphabet)]
	}
	return string(b)
}

// freshRoundID picks a large random on-chain round id, retrying on the (extremely unlikely)
// chance it collides with an existing round.
func freshRoundID(reader *chain.Client) (*big.Int, error) {
	for i := 0; i < 5; i++ {
		buf := make([]byte, 8)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		n := new(big.Int).SetBytes(buf)
		n.Rsh(n, 1) // keep it comfortably positive / within uint256 with headroom
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		exists, err := reader.RoundExists(ctx, n)
		cancel()
		if err != nil {
			return nil, err
		}
		if !exists {
			return n, nil
		}
	}
	return nil, fmt.Errorf("could not find a free round id")
}

// --- room ops (each returns a fresh snapshot for the caller) ---

func (m *Manager) Join(code, playerID, name string) (*RoomView, error) {
	r, ok := m.room(code)
	if !ok {
		return nil, errRoom("room not found")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, in := r.players[playerID]; in {
		return r.snapshotLocked(playerID), nil
	}
	if r.state != "lobby" {
		return nil, errRoom("this race already started")
	}
	if len(r.players) >= MaxPlayers {
		return nil, errRoom("room is full")
	}
	if r.stake != nil {
		if !common.IsHexAddress(playerID) {
			return nil, errRoom("this room is staked — connect your wallet first")
		}
		if m.reader == nil {
			return nil, errRoom("staking isn't available on this server right now")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		entered, err := m.reader.HasEntered(ctx, r.roundID, common.HexToAddress(playerID))
		cancel()
		if err != nil {
			return nil, errRoom("could not verify your stake: " + err.Error())
		}
		if !entered {
			return nil, errRoom("stake first: approve and enter on-chain before joining")
		}
	}
	r.addPlayer(playerID, name)
	return r.snapshotLocked(playerID), nil
}

func (m *Manager) Start(code, playerID string) (*RoomView, error) {
	r, ok := m.room(code)
	if !ok {
		return nil, errRoom("room not found")
	}
	r.mu.Lock()
	if playerID != r.host {
		r.mu.Unlock()
		return nil, errRoom("only the host can start")
	}
	if r.state != "lobby" {
		r.mu.Unlock()
		return nil, errRoom("already started")
	}
	r.letters = rack.GenerateSolo(r.dict, rackSize).Letters
	r.state = "racing"
	r.endsAt = time.Now().Add(time.Duration(r.raceSeconds) * time.Second)
	view := r.snapshotLocked(playerID)
	r.mu.Unlock()

	if r.stake != nil {
		m.scheduleSettle(r)
	}
	return view, nil
}

// Submit scores one word for a player. Returns (accepted, points, snapshot).
func (m *Manager) Submit(code, playerID, word string) (bool, int, *RoomView, error) {
	r, ok := m.room(code)
	if !ok {
		return false, 0, nil, errRoom("room not found")
	}
	r.mu.Lock()
	r.maybeFinishLocked()
	if r.state != "racing" {
		v := r.snapshotLocked(playerID)
		r.mu.Unlock()
		return false, 0, v, errRoom("race is not running")
	}
	p, in := r.players[playerID]
	if !in {
		r.mu.Unlock()
		return false, 0, nil, errRoom("you're not in this room")
	}
	w := strings.ToUpper(strings.TrimSpace(word))
	if p.found[w] {
		v := r.snapshotLocked(playerID)
		r.mu.Unlock()
		return false, 0, v, nil // duplicate — silently not counted
	}
	res := game.Score(r.dict, r.letters, []string{w})
	if len(res.Accepted) != 1 {
		v := r.snapshotLocked(playerID)
		r.mu.Unlock()
		return false, 0, v, nil
	}
	pts := res.Accepted[0].Points
	p.found[w] = true
	p.score += pts
	p.words++
	v := r.snapshotLocked(playerID)
	r.mu.Unlock()
	return true, pts, v, nil
}

// --- staking: automatic settlement when a race ends ---

// scheduleSettle fires settlement at max(now, stakeEndsAt) — the contract won't accept
// settle() until its on-chain entry window has closed, so a race that finishes early just
// waits out the remainder of that window before the payout broadcasts.
func (m *Manager) scheduleSettle(r *Room) {
	delay := time.Until(r.stakeEndsAt) + 2*time.Second
	if delay < 0 {
		delay = 0
	}
	time.AfterFunc(delay, func() { m.settleRoom(r) })
}

func (m *Manager) settleRoom(r *Room) {
	r.mu.Lock()
	r.maybeFinishLocked()
	if r.state != "done" || r.settleStatus != "" {
		r.mu.Unlock()
		return
	}
	r.settleStatus = "pending"
	winnerID := r.leaderLocked()
	pot := new(big.Int).Mul(r.stake, big.NewInt(int64(len(r.players))))
	roundID := r.roundID
	r.mu.Unlock()

	if winnerID == "" || !common.IsHexAddress(winnerID) {
		r.mu.Lock()
		r.settleStatus = "failed"
		r.settleErr = "no eligible winner"
		r.mu.Unlock()
		return
	}

	winners := []common.Address{common.HexToAddress(winnerID)}
	amounts := []*big.Int{pot}

	sig, err := m.signer.SignSettlement(roundID, winners, amounts)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = m.writer.Settle(ctx, roundID, winners, amounts, sig)
		cancel()
	}

	r.mu.Lock()
	if err != nil {
		r.settleStatus = "failed"
		r.settleErr = err.Error()
	} else {
		r.settleStatus = "settled"
	}
	r.mu.Unlock()
}

// --- internals ---

func (r *Room) addPlayer(id, name string) {
	if name == "" {
		name = shortID(id)
	}
	r.players[id] = &player{id: id, name: name, found: map[string]bool{}}
	r.order = append(r.order, id)
}

func (r *Room) maybeFinishLocked() {
	if r.state == "racing" && !time.Now().Before(r.endsAt) {
		r.state = "done"
	}
}

// leaderLocked returns the top-scoring player id (r.mu must be held).
func (r *Room) leaderLocked() string {
	ids := rankedIDsLocked(r)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func rankedIDsLocked(r *Room) []string {
	ids := append([]string(nil), r.order...)
	pos := make(map[string]int, len(r.order))
	for i, id := range r.order {
		pos[id] = i
	}
	sort.SliceStable(ids, func(a, b int) bool {
		pa, pb := r.players[ids[a]], r.players[ids[b]]
		if pa.score != pb.score {
			return pa.score > pb.score
		}
		return pos[ids[a]] < pos[ids[b]]
	})
	return ids
}

func (r *Room) snapshot(viewer string) *RoomView {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked(viewer)
}

func (r *Room) snapshotLocked(viewer string) *RoomView {
	r.maybeFinishLocked()
	ids := rankedIDsLocked(r)

	pv := make([]PlayerView, len(ids))
	for i, id := range ids {
		p := r.players[id]
		pv[i] = PlayerView{ID: id, Name: p.name, Score: p.score, Words: p.words}
	}

	timeLeft := 0
	if r.state == "racing" {
		if d := int(time.Until(r.endsAt).Seconds()); d > 0 {
			timeLeft = d
		}
	}
	letters := ""
	if r.state != "lobby" {
		letters = r.letters
	}
	winner := ""
	if r.state == "done" && len(ids) > 0 {
		winner = ids[0]
	}

	stake, pot, roundID, stakeEndsIn := "0", "0", "", 0
	if r.stake != nil {
		stake = r.stake.String()
		pot = new(big.Int).Mul(r.stake, big.NewInt(int64(len(ids)))).String()
		roundID = r.roundID.String()
		if d := int(time.Until(r.stakeEndsAt).Seconds()); d > 0 {
			stakeEndsIn = d
		}
	}

	return &RoomView{
		Code: r.code, State: r.state, Host: r.host, Public: r.public, Letters: letters,
		TimeLeft: timeLeft, Players: pv, Winner: winner, You: viewer,
		Stake: stake, Pot: pot, RoundID: roundID, StakeEndsIn: stakeEndsIn,
		SettleStatus: r.settleStatus, SettleErr: r.settleErr,
	}
}

func shortID(id string) string {
	if len(id) >= 6 && strings.HasPrefix(id, "0x") {
		return id[:6] + "…" + id[len(id)-4:]
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

type roomErr string

func (e roomErr) Error() string { return string(e) }
func errRoom(s string) error    { return roomErr(s) }

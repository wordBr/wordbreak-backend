// Command server runs the WordBreak backend: the game API + the referee signer.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/wordbreak/backend/internal/api"
	"github.com/wordbreak/backend/internal/chain"
	"github.com/wordbreak/backend/internal/dictionary"
	"github.com/wordbreak/backend/internal/signer"
	"github.com/wordbreak/backend/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[wordbreak] ")

	port := env("PORT", "8080")

	log.Println("loading dictionary...")
	dict := dictionary.New()
	log.Printf("dictionary loaded: %d words", dict.Size())

	// The referee signer is optional: without a key the game still runs, but settlement
	// signing is disabled. This lets you develop the game loop before wiring up money.
	var sg *signer.Signer
	if pk := os.Getenv("REFEREE_PRIVATE_KEY"); pk != "" {
		contract := os.Getenv("POOLS_CONTRACT")
		if contract == "" {
			log.Fatal("REFEREE_PRIVATE_KEY set but POOLS_CONTRACT missing")
		}
		chainID := envInt64("CHAIN_ID", 42220) // Celo mainnet
		s, err := signer.New(pk, chainID, contract)
		if err != nil {
			log.Fatalf("referee signer: %v", err)
		}
		sg = s
		log.Printf("referee configured: %s (chainId=%d, pool=%s)", sg.Address().Hex(), chainID, contract)
	} else {
		log.Println("no REFEREE_PRIVATE_KEY — settlement signing disabled")
	}

	// Read-only chain client: lets the referee verify on-chain pool entry before scoring or
	// paying anyone. Required for paid dailies; without it, paid submissions are refused.
	var chainCli *chain.Client
	if rpc := os.Getenv("CHAIN_RPC_URL"); rpc != "" {
		contract := os.Getenv("POOLS_CONTRACT")
		if contract == "" {
			log.Fatal("CHAIN_RPC_URL set but POOLS_CONTRACT missing")
		}
		c, err := chain.New(rpc, contract)
		if err != nil {
			log.Fatalf("chain client: %v", err)
		}
		chainCli = c
		defer chainCli.Close()
		log.Printf("chain verification enabled via %s", rpc)
	} else {
		log.Println("no CHAIN_RPC_URL — paid dailies disabled (solo + unpaid daily only)")
	}

	srv := api.New(dict, store.New(), sg, chainCli, api.Config{
		SoloRackSize:  envInt("SOLO_RACK_SIZE", 6),
		DailyRackSize: envInt("DAILY_RACK_SIZE", 6),
		AdminToken:    os.Getenv("ADMIN_TOKEN"),
	})

	// Staked multiplayer rooms need a funded operator key that can broadcast createRound
	// (must be the pool's owner or referee) and settle (any funded account — the EIP-712
	// signature is the authorization, not the sender). Optional: without it, staked rooms
	// are rejected with a clear error but free rooms still work.
	if pk := os.Getenv("OPERATOR_PRIVATE_KEY"); pk != "" && sg != nil && chainCli != nil {
		contract := os.Getenv("POOLS_CONTRACT")
		rpc := os.Getenv("CHAIN_RPC_URL")
		chainID := envInt64("CHAIN_ID", 42220)
		w, err := chain.NewWriter(rpc, contract, pk, chainID)
		if err != nil {
			log.Fatalf("room staking operator: %v", err)
		}
		defer w.Close()
		srv.EnableRoomStaking(w, chainCli, sg)
		log.Printf("staked multiplayer rooms enabled, operator: %s", w.Address().Hex())
	} else {
		log.Println("no OPERATOR_PRIVATE_KEY — staked multiplayer rooms disabled (free rooms still work)")
	}

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	go func() {
		log.Printf("listening on :%s", port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

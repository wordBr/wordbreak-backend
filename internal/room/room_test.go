package room

import (
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/wordbreak/backend/internal/dictionary"
)

const (
	alice = "0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1"
	bob   = "0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB2"
)

func mustCreate(t *testing.T, m *Manager, id, name string, opts CreateOpts) *RoomView {
	t.Helper()
	v, err := m.Create(id, name, opts)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return v
}

func TestRace_HappyPath(t *testing.T) {
	os.Setenv("RACE_SECONDS", "2") // short race so we can assert the winner
	defer os.Unsetenv("RACE_SECONDS")

	dict := dictionary.New()
	m := NewManager(dict)

	// create → lobby with the host
	v := mustCreate(t, m, alice, "Alice", CreateOpts{})
	code := v.Code
	if v.State != "lobby" || len(v.Players) != 1 || v.Host != alice {
		t.Fatalf("create: %+v", v)
	}

	// join → 2 players
	if v, err := m.Join(code, bob, "Bob"); err != nil || len(v.Players) != 2 {
		t.Fatalf("join: %+v err=%v", v, err)
	}

	// only the host can start
	if _, err := m.Start(code, bob); err == nil {
		t.Fatal("non-host was allowed to start")
	}

	// host starts → racing, shared rack dealt
	sv, err := m.Start(code, alice)
	if err != nil || sv.State != "racing" || sv.Letters == "" || sv.TimeLeft <= 0 {
		t.Fatalf("start: %+v err=%v", sv, err)
	}
	words := dict.FindWords(sv.Letters)
	if len(words) < 2 {
		t.Fatalf("rack %q had too few words (%d)", sv.Letters, len(words))
	}

	// Alice finds several words; Bob finds one → Alice should lead
	aliceWords := words
	if len(aliceWords) > 4 {
		aliceWords = aliceWords[:4]
	}
	for _, w := range aliceWords {
		if ok, pts, _, err := m.Submit(code, alice, w); err != nil || !ok || pts <= 0 {
			t.Fatalf("alice submit %q: ok=%v pts=%d err=%v", w, ok, pts, err)
		}
	}
	if ok, _, _, err := m.Submit(code, bob, words[0]); err != nil || !ok {
		t.Fatalf("bob submit: ok=%v err=%v", ok, err)
	}

	// duplicate and junk are not counted
	if ok, _, _, _ := m.Submit(code, alice, aliceWords[0]); ok {
		t.Fatal("duplicate word was counted")
	}
	if ok, _, _, _ := m.Submit(code, alice, "ZZZQXJ"); ok {
		t.Fatal("invalid word was counted")
	}

	// live standings: Alice on top, scored server-side
	snap, _ := m.Get(code, alice)
	if snap.Players[0].ID != alice {
		t.Fatalf("expected Alice leading, got %+v", snap.Players)
	}
	if snap.Players[0].Score <= snap.Players[1].Score {
		t.Fatalf("leader score not ahead: %+v", snap.Players)
	}

	// race ends → winner declared
	time.Sleep(2200 * time.Millisecond)
	done, _ := m.Get(code, alice)
	if done.State != "done" {
		t.Fatalf("expected done, got %q", done.State)
	}
	if done.Winner != alice {
		t.Fatalf("expected winner Alice, got %q", done.Winner)
	}
}

func TestRace_RoomFullAndLateJoin(t *testing.T) {
	dict := dictionary.New()
	m := NewManager(dict)
	v := mustCreate(t, m, "p0", "", CreateOpts{})
	for i := 1; i < MaxPlayers; i++ {
		if _, err := m.Join(v.Code, "p"+string(rune('0'+i)), ""); err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
	}
	// 6th player rejected
	if _, err := m.Join(v.Code, "p9", ""); err == nil {
		t.Fatal("6th player was allowed into a full room")
	}
	// can't join after start
	m.Start(v.Code, "p0")
	if _, err := m.Join("XXXX", "late", ""); err == nil {
		t.Fatal("join to unknown room succeeded")
	}
}

func TestPublicRooms_ListedAndFilterable(t *testing.T) {
	dict := dictionary.New()
	m := NewManager(dict)

	pub := mustCreate(t, m, "host-pub", "Host", CreateOpts{Public: true})
	priv := mustCreate(t, m, "host-priv", "Host", CreateOpts{Public: false})

	listed := m.List()
	found := map[string]bool{}
	for _, v := range listed {
		found[v.Code] = true
	}
	if !found[pub.Code] {
		t.Fatal("public room missing from List()")
	}
	if found[priv.Code] {
		t.Fatal("private room leaked into List()")
	}

	// a full public room drops off the list
	for i := 1; i < MaxPlayers; i++ {
		if _, err := m.Join(pub.Code, "p"+string(rune('0'+i)), ""); err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
	}
	listed = m.List()
	for _, v := range listed {
		if v.Code == pub.Code {
			t.Fatal("full public room still listed")
		}
	}

	// once racing starts, it also drops off (state != lobby)
	pub2 := mustCreate(t, m, "host2", "", CreateOpts{Public: true})
	m.Start(pub2.Code, "host2")
	for _, v := range m.List() {
		if v.Code == pub2.Code {
			t.Fatal("racing room still listed as joinable")
		}
	}
}

func TestStaking_RejectedWithoutOperatorConfigured(t *testing.T) {
	dict := dictionary.New()
	m := NewManager(dict) // EnableStaking never called

	_, err := m.Create(alice, "Alice", CreateOpts{Stake: big.NewInt(1)})
	if err == nil {
		t.Fatal("staked room was created with no staking operator configured")
	}
}

func TestStaking_RejectsNonWalletID(t *testing.T) {
	dict := dictionary.New()
	m := NewManager(dict)
	_, err := m.Create("not-a-wallet", "Alice", CreateOpts{Stake: big.NewInt(1)})
	if err == nil {
		t.Fatal("staked room was created for a non-wallet player id")
	}
}

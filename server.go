package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
)

// Session

type session struct {
	mu            sync.Mutex
	chars         []character
	transcript    []turn
	systemPrompts map[string]string
	readingSlug   string
}

var (
	sessionsMu sync.Mutex
	sessions   = map[string]*session{}
)

func newSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Request/Response

type startRequest struct {
	Characters []string `json:"characters"`
	Reading    string   `json:"reading"`
}

type startResponse struct {
	SessionID string `json:"session_id"`
}

type messageRequest struct {
	Message string `json:"message"`
}

type replyItem struct {
	Speaker string `json:"speaker"`
	Content string `json:"content"`
}

type messageResponse struct {
	Replies []replyItem `json:"replies"`
	Passed  []string    `json:"passed"`
}

type endResponse struct {
	Updated []string `json:"updated"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type transcriptResponse struct {
	Transcript []turn `json:"transcript"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func handleStart(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Characters) == 0 {
		writeError(w, http.StatusBadRequest, "at least one character is required")
		return
	}

	os.MkdirAll("characters/_relations", 0755)

	var chars []character
	for _, d := range req.Characters {
		chars = append(chars, loadCharacter(d, req.Reading))
	}

	systemPrompts := make(map[string]string)
	for _, c := range chars {
		var others []character
		for _, o := range chars {
			if o.dir != c.dir {
				others = append(others, o)
			}
		}
		ctx := promptContext{
			Notes:        c.notes,
			Topics:       c.topics,
			UserRelation: c.userRelation,
			Reputation:   buildReputationBlock(c, others),
			Relation:     buildRelationBlock(c, others),
			Reading:      c.reading,
		}
		sp := buildSystemPrompt(c.core, ctx)
		if len(chars) > 1 {
			sp += diegeticFraming + passInstruction + copresenceFraming
		}
		systemPrompts[c.core.Name] = sp
	}

	s := &session{
		chars:         chars,
		systemPrompts: systemPrompts,
		readingSlug:   req.Reading,
	}

	id := newSessionID()
	sessionsMu.Lock()
	sessions[id] = s
	sessionsMu.Unlock()

	writeJSON(w, http.StatusOK, startResponse{SessionID: id})
}

func getSession(w http.ResponseWriter, r *http.Request) (*session, bool) {
	id := r.PathValue("id")
	sessionsMu.Lock()
	s, ok := sessions[id]
	sessionsMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return nil, false
	}
	return s, true
}

func handleMessage(w http.ResponseWriter, r *http.Request) {
	s, ok := getSession(w, r)
	if !ok {
		return
	}

	var req messageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.transcript = append(s.transcript, turn{Speaker: "User", Content: req.Message})

	resp := messageResponse{}

	runRound := func() {
		for _, c := range s.chars {
			reply := callClaude(s.systemPrompts[c.core.Name], toAPIMessages(s.transcript, c.core.Name), 1024)
			if isPass(reply) {
				resp.Passed = append(resp.Passed, c.core.Name)
				continue
			}
			s.transcript = append(s.transcript, turn{Speaker: c.core.Name, Content: reply})
			resp.Replies = append(resp.Replies, replyItem{Speaker: c.core.Name, Content: reply})
		}
	}

	runRound()
	if len(s.chars) > 1 {
		runRound() // second round: reactions to what just happened
	}

	writeJSON(w, http.StatusOK, resp)
}

func handleEnd(w http.ResponseWriter, r *http.Request) {
	s, ok := getSession(w, r)
	if !ok {
		return
	}

	s.mu.Lock()
	updated := closeSalonSessionCollect(s.chars, s.transcript, s.readingSlug)
	s.mu.Unlock()

	sessionsMu.Lock()
	delete(sessions, r.PathValue("id"))
	sessionsMu.Unlock()

	writeJSON(w, http.StatusOK, endResponse{Updated: updated})
}

func handleTranscript(w http.ResponseWriter, r *http.Request) {
	s, ok := getSession(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, transcriptResponse{Transcript: s.transcript})
}

func closeSalonSessionCollect(chars []character, transcript []turn, bookSlug string) []string {
	var updated []string
	if len(transcript) == 0 {
		return updated
	}
	full := formatMultiTranscript(transcript)

	for _, c := range chars {
		if delta := extractDelta(c.core.Name, full); notEmpty(delta) {
			appendNotes("characters/"+c.dir+"/notes.md", c.core.Name, delta)
			updated = append(updated, c.core.Name+"'s notes")
		}
		if topics := extractTopics(c.core.Name, full); notEmpty(topics) {
			appendNotes("characters/"+c.dir+"/topics.md", c.core.Name, topics)
			updated = append(updated, c.core.Name+"'s topics")
		}
		if userRel := extractUserRelationalDelta(c.core.Name, full); notEmpty(userRel) {
			appendNotes(userRelationPath(c.core.Name), c.core.Name, userRel)
			updated = append(updated, c.core.Name+"'s relationship-with-you notes")
		}

		if bookSlug != "" {
			progressPath := readingProgressPath(c.dir, bookSlug)
			if progressBytes, err := os.ReadFile(progressPath); err == nil {
				structure, priorReaction := splitProgress(string(progressBytes))
				newReaction := extractDiscussionReactionUpdate(c.core.Name, coreContextSummary(c.core), bookSlug, priorReaction, full)
				combined := "# STRUCTURE\n\n" + structure + "\n\n# REACTION\n\n" + newReaction
				if err := os.WriteFile(progressPath, []byte(combined), 0644); err == nil {
					updated = append(updated, c.core.Name+"'s reading of "+bookSlug)
				}
			}
		}
	}

	var names []string
	for _, c := range chars {
		names = append(names, c.core.Name)
	}
	for _, p := range pairs(names) {
		relDelta := extractRelationalDelta(p[0], p[1], full)
		if notEmpty(relDelta) {
			appendNotes(relationPath(p[0], p[1]), p[0]+"/"+p[1], relDelta)
			updated = append(updated, "relational: "+p[0]+"/"+p[1])
		}
	}
	return updated
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func runServer(port string) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /session/start", handleStart)
	mux.HandleFunc("POST /session/{id}/message", handleMessage)
	mux.HandleFunc("POST /session/{id}/end", handleEnd)
	mux.HandleFunc("GET /session/{id}/transcript", handleTranscript)
	fmt.Println("listening on :" + port)
	http.ListenAndServe(":"+port, withCORS(mux))
}

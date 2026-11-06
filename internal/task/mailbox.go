package task

// Messages from the person to a running task's team. The task may run in
// another process (detached), which keeps task.json in memory and rewrites
// it, so a message is never written into task.json from outside. Each one
// is its own file in the task's mailbox directory. The runner collects
// them under its lock, moves them into the task's inbox, and delivers them
// like teammates' messages, with each recipient's next model request.
//
// The mailbox is in ackn4th's data directory, outside the workspace and
// every agent's scratch directory, so neither the file tools nor sandboxed
// commands can write to it: agents can't forge a message from the person.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dethmasque/ackn4th/internal/event"
	"github.com/dethmasque/ackn4th/internal/tools"
)

// MaxUserMessage is the longest message, in characters, the person can
// send a running team.
const MaxUserMessage = 8000

// maxPostFile bounds what the runner reads from one mailbox file.
const maxPostFile = 64 << 10

// post is one message file in the mailbox.
type post struct {
	To   string    `json:"to"` // a role, or "all"
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

// MailboxDir is where messages to t's running team wait to be collected.
func (s *Store) MailboxDir(id string) string { return filepath.Join(s.Dir(id), "to-team") }

// MessageTeam leaves a message from the person for task id's team: to is
// a role or "all". The running task collects it and delivers it to each
// agent it names with that agent's next request; the task keeps running.
// A message left while the task isn't running waits for its next run.
func (s *Store) MessageTeam(id, to, text string, now time.Time) error {
	text = strings.TrimSpace(text)
	to = strings.TrimSpace(to)
	switch {
	case text == "":
		return errors.New("empty message")
	case utf8.RuneCountInString(text) > MaxUserMessage:
		return fmt.Errorf("the message is over %d characters: shorten it, or put the details in a file in the workspace and point to it", MaxUserMessage)
	case to == "":
		to = "all"
	}
	data, err := json.Marshal(post{To: to, Text: text, Time: now.UTC()})
	if err != nil {
		return err
	}
	dir := s.MailboxDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var b [6]byte
	rand.Read(b[:])
	// Names sort by time, so messages are delivered in the order sent.
	// writeAtomic writes a dot-file first and renames it: the runner skips
	// dot-files, so it never reads a message half written.
	return writeAtomic(filepath.Join(dir, fmt.Sprintf("%020d-%s.json", now.UnixNano(), hex.EncodeToString(b[:]))), data)
}

// collect moves the person's waiting messages into the task's inbox. Call
// it with rn.mu held. A message is saved in the inbox before its file is
// removed, so a crash in between delivers it twice rather than never.
func (rn *run) collect() {
	dir := rn.r.Store.MailboxDir(rn.t.ID)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return
	}
	var done []string
	for _, e := range entries { // ReadDir sorts by name: by time sent
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") || !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, name)
		done = append(done, path)
		p, err := readPost(path)
		if err != nil {
			rn.r.emit(rn.t, event.Event{Kind: event.Notice, Text: "a message from you couldn't be read and was dropped: " + err.Error()})
			continue
		}
		targets := tools.Targets(rn.members, p.To)
		if len(targets) == 0 {
			rn.r.emit(rn.t, event.Event{Kind: event.Notice, Text: fmt.Sprintf("your message to %q was dropped: no such agent (use one of %s, or all)", p.To, strings.Join(rn.members, ", "))})
			continue
		}
		for _, n := range targets {
			rn.t.Inbox = append(rn.t.Inbox, Mail{ID: nextMailID(rn.t.Inbox), User: true, To: n, Round: rn.t.Round, Text: p.Text, Time: p.Time})
		}
		rn.r.emit(rn.t, event.Event{Kind: event.UserMessage, Text: p.Text, Detail: p.To})
	}
	if len(done) == 0 {
		return
	}
	if err := rn.r.Store.Save(rn.t); err != nil {
		return // the files stay, and are collected again next time
	}
	for _, path := range done {
		os.Remove(path)
	}
}

// readPost reads and checks one mailbox file.
func readPost(path string) (post, error) {
	var p post
	f, err := os.Open(path)
	if err != nil {
		return p, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPostFile+1))
	if err != nil {
		return p, err
	}
	if len(data) > maxPostFile {
		return p, errors.New("too large")
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	p.Text = strings.TrimSpace(p.Text)
	if p.Text == "" || utf8.RuneCountInString(p.Text) > MaxUserMessage {
		return p, errors.New("empty or too long")
	}
	if p.Time.IsZero() {
		p.Time = time.Now().UTC()
	}
	return p, nil
}

func nextMailID(inbox []Mail) int {
	id := 1
	for _, m := range inbox {
		id = max(id, m.ID+1)
	}
	return id
}

// userMailWaiting reports whether a message from the person hasn't reached
// its agent yet. Call it with rn.mu held.
func (rn *run) userMailWaiting() bool {
	return slices.ContainsFunc(rn.t.Inbox, func(m Mail) bool { return m.User })
}

// agreed reports whether the team has finished: every agent declared
// done, and no message from the person is still on its way. When the
// votes are in but a message hasn't been read, ending would lose it, so
// it calls off the vote: everyone must declare done again once it has
// been read.
func (rn *run) agreed() bool {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	rn.collect()
	if !allDone(rn.t) {
		return false // nothing would end: a waiting message arrives in time
	}
	if rn.userMailWaiting() {
		for i := range rn.t.Agents {
			rn.t.Agents[i].Done = false
		}
		return false
	}
	return true
}

package herdr

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/dethlex/GopherClaude/internal/domain"
)

// herdr pane list, in the real shape: a Claude pane whose agent_session is
// the registry session id, an agy pane Herdr recognised but without a
// session id, and a plain shell pane in the same project as the agy one.
const paneList = `{"id":"cli:pane:list","result":{"panes":[` +
	`{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"sess-a"},"agent_status":"idle","cwd":"/Users/x/proj-a","focused":false,"pane_id":"w1:p1","tab_id":"w1:t1","workspace_id":"w1"},` +
	`{"agent":"agy","agent_status":"working","cwd":"/Users/x/proj-b","focused":false,"pane_id":"w1:p2","tab_id":"w1:t2","workspace_id":"w1"},` +
	`{"cwd":"/Users/x/proj-b","focused":false,"pane_id":"w2:p3","tab_id":"w2:t3","workspace_id":"w2"}` +
	`],"type":"pane_list"}}`

// process-info replies keyed by pane: the agy pane's foreground group is a
// wrapper, the agy process itself is further down the list; the shell pane
// runs a plain codex as its foreground group.
var processInfo = map[string]string{
	"w1:p2": `{"id":"cli:pane:process_info","result":{"process_info":{"foreground_process_group_id":5000,"foreground_processes":[{"argv":["caffeinate"],"cwd":"/Users/x/proj-b","name":"caffeinate","pid":5000},{"argv":["agy"],"cwd":"/Users/x/proj-b","name":"agy","pid":4242}],"pane_id":"w1:p2","shell_pid":4000}}}`,
	"w2:p3": `{"id":"cli:pane:process_info","result":{"process_info":{"foreground_process_group_id":7777,"foreground_processes":[{"argv":["codex"],"cwd":"/Users/x/proj-b","name":"codex","pid":7777}],"pane_id":"w2:p3","shell_pid":7000}}}`,
}

type fakeHerdr struct {
	calls      []string
	agentFocus map[string]bool // pane ids agent focus accepts
	listErr    error
}

func (h *fakeHerdr) run(_ context.Context, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	h.calls = append(h.calls, call)

	switch {
	case call == "pane list":
		if h.listErr != nil {
			return nil, h.listErr
		}

		return []byte(paneList), nil
	case strings.HasPrefix(call, "pane process-info --pane "):
		pane := strings.TrimPrefix(call, "pane process-info --pane ")
		if reply, ok := processInfo[pane]; ok {
			return []byte(reply), nil
		}

		return nil, errors.New(`{"error":{"code":"pane_not_found"}}`)
	case strings.HasPrefix(call, "agent focus "):
		if h.agentFocus[strings.TrimPrefix(call, "agent focus ")] {
			return []byte(`{"id":"cli:agent:focus","result":{}}`), nil
		}

		return nil, errors.New(`{"error":{"code":"agent_not_found","message":"agent target not found"}}`)
	case strings.HasPrefix(call, "tab focus "):
		return []byte(`{"id":"cli:tab:focus","result":{}}`), nil
	default:
		return nil, errors.New("unexpected herdr call: " + call)
	}
}

type fakeNext struct {
	calls   int
	targets []domain.FocusTarget
	err     error
}

func (n *fakeNext) Focus(t domain.FocusTarget) error {
	n.calls++
	n.targets = append(n.targets, t)

	return n.err
}

func newTestFocuser(h *fakeHerdr, next *fakeNext) *Focuser {
	return &Focuser{
		run:     h.run,
		next:    next,
		clients: func(context.Context) []int { return nil },
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()

	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("herdr calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestFocusBySessionID(t *testing.T) {
	h := &fakeHerdr{agentFocus: map[string]bool{"w1:p1": true}}
	next := &fakeNext{}

	err := newTestFocuser(h, next).Focus(domain.FocusTarget{PID: 111, Dir: "/Users/x/proj-a", SessionID: "sess-a"})
	if err != nil {
		t.Fatal(err)
	}

	assertCalls(t, h.calls, []string{"pane list", "agent focus w1:p1"})

	if next.calls != 1 {
		t.Errorf("app focuser called %d times, want 1 (it raises the Herdr window)", next.calls)
	}
}

// Without a session id the pane is found by pid, but only panes in the
// session's directory are asked: process-info is one herdr call each.
func TestFocusByPIDAmongPanesOfTheSameDir(t *testing.T) {
	h := &fakeHerdr{agentFocus: map[string]bool{"w1:p2": true}}
	next := &fakeNext{}

	err := newTestFocuser(h, next).Focus(domain.FocusTarget{PID: 4242, Dir: "/Users/x/proj-b", SessionID: "conv-42"})
	if err != nil {
		t.Fatal(err)
	}

	assertCalls(t, h.calls, []string{"pane list", "pane process-info --pane w1:p2", "agent focus w1:p2"})
}

// A pane Herdr does not treat as an agent still has a tab to jump to.
func TestFocusFallsBackToTabForPlainPane(t *testing.T) {
	h := &fakeHerdr{}
	next := &fakeNext{}

	err := newTestFocuser(h, next).Focus(domain.FocusTarget{PID: 7777, Dir: "/Users/x/proj-b", SessionID: "thread-7"})
	if err != nil {
		t.Fatal(err)
	}

	assertCalls(t, h.calls, []string{
		"pane list",
		"pane process-info --pane w1:p2",
		"pane process-info --pane w2:p3",
		"agent focus w2:p3",
		"tab focus w2:t3",
	})
}

// No pane hosts the session (Claude Desktop, a foreign terminal): the app
// focuser decides, and its verdict is the result.
func TestFocusFallsBackToAppWhenNoPane(t *testing.T) {
	h := &fakeHerdr{}
	next := &fakeNext{err: errors.New("no GUI app owns pid 9")}

	err := newTestFocuser(h, next).Focus(domain.FocusTarget{PID: 9, Dir: "/Users/x/elsewhere", SessionID: "desk"})
	if err == nil || !strings.Contains(err.Error(), "no GUI app") {
		t.Errorf("Focus = %v, want the app focuser's error", err)
	}

	assertCalls(t, h.calls, []string{"pane list"})

	if next.calls != 1 {
		t.Errorf("app focuser called %d times, want 1", next.calls)
	}
}

// Herdr not running (or its CLI missing) must not cost the jump: the app
// focuser still runs and its success is a success.
func TestFocusSurvivesHerdrFailure(t *testing.T) {
	h := &fakeHerdr{listErr: errors.New("connection refused")}
	next := &fakeNext{}

	if err := newTestFocuser(h, next).Focus(domain.FocusTarget{PID: 111, Dir: "/Users/x/proj-a", SessionID: "sess-a"}); err != nil {
		t.Errorf("Focus = %v, want nil when the app focuser succeeds", err)
	}

	if next.calls != 1 {
		t.Errorf("app focuser called %d times, want 1", next.calls)
	}
}

// Once the pane is focused the user is where they wanted to be; a failing
// `open` afterwards is noise, not an error.
func TestFocusIgnoresAppErrorAfterPaneFocus(t *testing.T) {
	h := &fakeHerdr{agentFocus: map[string]bool{"w1:p1": true}}
	next := &fakeNext{err: errors.New("open failed")}

	if err := newTestFocuser(h, next).Focus(domain.FocusTarget{PID: 111, Dir: "/Users/x/proj-a", SessionID: "sess-a"}); err != nil {
		t.Errorf("Focus = %v, want nil after the pane was focused", err)
	}
}

func TestDefaultBinPrefersEnvThenPath(t *testing.T) {
	t.Setenv(EnvBinPath, "/somewhere/herdr")

	if got := DefaultBin(); got != "/somewhere/herdr" {
		t.Errorf("DefaultBin with %s set = %q", EnvBinPath, got)
	}

	t.Setenv(EnvBinPath, "")

	// Whatever PATH and the Homebrew default yield, an empty result means
	// the feature is off; it must never be a non-existent path.
	if got := DefaultBin(); got != "" && !strings.HasSuffix(got, "/herdr") {
		t.Errorf("DefaultBin = %q, want a herdr binary path or empty", got)
	}
}

// Herdr is a TUI: the pane's process tree ends at the herdr server under
// launchd, so raising the window means opening the terminal that hosts an
// attached herdr client. After the pane is focused the app focuser gets
// that client's pid instead of the session's.
func TestFocusRaisesTheTerminalOfTheHerdrClient(t *testing.T) {
	h := &fakeHerdr{agentFocus: map[string]bool{"w1:p1": true}}
	next := &fakeNext{}

	f := newTestFocuser(h, next)
	f.clients = func(context.Context) []int { return []int{73409} }

	if err := f.Focus(domain.FocusTarget{PID: 111, Dir: "/Users/x/proj-a", SessionID: "sess-a"}); err != nil {
		t.Fatal(err)
	}

	if len(next.targets) != 1 || next.targets[0].PID != 73409 || next.targets[0].Dir != "/Users/x/proj-a" {
		t.Errorf("app focuser targets = %+v, want the herdr client pid 73409 with the session dir", next.targets)
	}
}

// Without an attached client (or outside Herdr) the app focuser keeps the
// session's own pid.
func TestFocusKeepsSessionPIDWithoutClient(t *testing.T) {
	h := &fakeHerdr{agentFocus: map[string]bool{"w1:p1": true}}
	next := &fakeNext{}

	if err := newTestFocuser(h, next).Focus(domain.FocusTarget{PID: 111, Dir: "/Users/x/proj-a", SessionID: "sess-a"}); err != nil {
		t.Fatal(err)
	}

	if len(next.targets) != 1 || next.targets[0].PID != 111 {
		t.Errorf("app focuser targets = %+v, want the session pid", next.targets)
	}
}

// A herdr CLI that cannot answer (protocol mismatch after an update, server
// down) is worth one warning per distinct error, not one per button press
// and not silence.
func TestFocusWarnsOnceWhenHerdrUnavailable(t *testing.T) {
	var logs bytes.Buffer

	h := &fakeHerdr{listErr: errors.New("protocol_mismatch: client protocol 22 is newer than server protocol 20")}
	next := &fakeNext{}

	f := newTestFocuser(h, next)
	f.logger = slog.New(slog.NewTextHandler(&logs, nil))

	target := domain.FocusTarget{PID: 111, Dir: "/Users/x/proj-a", SessionID: "sess-a"}

	for i := 0; i < 3; i++ {
		if err := f.Focus(target); err != nil {
			t.Fatal(err)
		}
	}

	if got := strings.Count(logs.String(), "level=WARN"); got != 1 {
		t.Errorf("WARN lines after 3 identical failures = %d, want 1:\n%s", got, logs.String())
	}

	if !strings.Contains(logs.String(), "protocol_mismatch") {
		t.Errorf("the warning must carry herdr's error text:\n%s", logs.String())
	}

	h.listErr = errors.New("connection refused")

	if err := f.Focus(target); err != nil {
		t.Fatal(err)
	}

	if got := strings.Count(logs.String(), "level=WARN"); got != 2 {
		t.Errorf("WARN lines after a different failure = %d, want 2:\n%s", got, logs.String())
	}
}

// ps -axo pid=,ppid=,tty=,comm= in the real shape: the server has no tty and
// hangs off launchd, the attached client sits on a terminal; unrelated rows
// and a shell whose name merely contains "herdr" are skipped.
func TestParseClients(t *testing.T) {
	ps := "54536     1 ??       /opt/homebrew/bin/herdr\n" +
		"73409 70841 ttys001  herdr\n" +
		"80001 70841 ttys002  /opt/homebrew/bin/herdr\n" +
		"35662 35089 ttys003  claude\n" +
		"90000 89999 ttys004  herdr-notes.sh\n"

	got := parseClients([]byte(ps))
	want := []int{73409, 80001}

	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("parseClients = %v, want %v", got, want)
	}
}

package host

import "testing"

func TestBundleFromExecutable(t *testing.T) {
	tests := []struct {
		name string
		exe  string
		want string
	}{
		{
			name: "iterm",
			exe:  "/Applications/iTerm.app/Contents/MacOS/iTerm2",
			want: "/Applications/iTerm.app",
		},
		{
			name: "claude desktop",
			exe:  "/Users/x/Library/Application Support/Claude/claude.app/Contents/MacOS/claude",
			want: "/Users/x/Library/Application Support/Claude/claude.app",
		},
		{
			name: "vscode",
			exe:  "/Applications/Visual Studio Code.app/Contents/MacOS/Electron",
			want: "/Applications/Visual Studio Code.app",
		},
		{
			name: "plain binary is not a bundle",
			exe:  "/opt/homebrew/bin/tmux",
			want: "",
		},
		{
			name: "empty",
			exe:  "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bundleFromExecutable(tt.exe); got != tt.want {
				t.Errorf("bundleFromExecutable(%q) = %q, want %q", tt.exe, got, tt.want)
			}
		})
	}
}

// fakeTree builds a procInfo from a pid -> (exe, ppid) map.
func fakeTree(nodes map[int]struct {
	exe  string
	ppid int
}) procInfo {
	return func(pid int) (string, int) {
		n, ok := nodes[pid]
		if !ok {
			return "", 0
		}

		return n.exe, n.ppid
	}
}

func TestOwningAppBundleClaudeDesktop(t *testing.T) {
	// Reproduces the real tree: the first .app going up is the embedded
	// claude-code helper; the window belongs to /Applications/Claude.app.
	tree := fakeTree(map[int]struct {
		exe  string
		ppid int
	}{
		100: {"/Users/x/Library/Application Support/Claude/claude-code/2.1.170/claude.app/Contents/MacOS/claude", 90},
		90:  {"/Applications/Claude.app/Contents/Helpers/disclaimer", 80},
		80:  {"/Applications/Claude.app/Contents/MacOS/Claude", 1},
	})

	if got := owningAppBundle(100, tree); got != "/Applications/Claude.app" {
		t.Errorf("owningAppBundle = %q, want /Applications/Claude.app (not the helper)", got)
	}
}

func TestOwningAppBundleTerminal(t *testing.T) {
	tree := fakeTree(map[int]struct {
		exe  string
		ppid int
	}{
		100: {"/opt/homebrew/bin/claude", 90},
		90:  {"/bin/zsh", 80},
		80:  {"/Applications/iTerm.app/Contents/MacOS/iTerm2", 1},
	})

	if got := owningAppBundle(100, tree); got != "/Applications/iTerm.app" {
		t.Errorf("owningAppBundle = %q, want /Applications/iTerm.app", got)
	}
}

func TestOwningAppBundleNone(t *testing.T) {
	tree := fakeTree(map[int]struct {
		exe  string
		ppid int
	}{
		100: {"/opt/homebrew/bin/claude", 90},
		90:  {"/opt/homebrew/bin/tmux", 1}, // detached: no GUI app
	})

	if got := owningAppBundle(100, tree); got != "" {
		t.Errorf("owningAppBundle = %q, want empty for a tmux session", got)
	}
}

func TestOwningAppBundleStopsOnCycle(t *testing.T) {
	// A self-referential parent must not loop forever.
	tree := fakeTree(map[int]struct {
		exe  string
		ppid int
	}{
		100: {"/opt/homebrew/bin/claude", 100},
	})

	if got := owningAppBundle(100, tree); got != "" {
		t.Errorf("owningAppBundle = %q, want empty", got)
	}
}

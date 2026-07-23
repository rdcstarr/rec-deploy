// Package privexec runs a shell command as another UNIX user, under a real
// timeout, capturing its output.
//
// The privilege drop goes through the kernel (syscall.Credential), not sudo:
// that removes the requirement for passwordless sudo to arbitrary users and the
// shell-quoting layer sudo drags along. The supplementary groups must be set
// explicitly — sudo -u gets them from PAM, SysProcAttr does not, and dropping
// them silently breaks group memberships HestiaCP relies on.
package privexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"time"
)

// TailBytes is how much of a command's combined output is kept. A deploy log is
// a diagnostic, not an archive: the tail is where the failure is.
const TailBytes = 8 << 10

// DefaultTimeout bounds a command that does not set its own.
const DefaultTimeout = 10 * time.Minute

// Options configures one command run.
type Options struct {
	// Dir is the working directory. Required.
	Dir string
	// User is the identity to drop to. nil runs as the current user.
	User *user.User
	// Env are extra KEY=VALUE entries appended to the derived environment.
	Env []string
	// Timeout bounds the command; DefaultTimeout when zero.
	Timeout time.Duration
	// Stream, when non-nil, receives the command's output live — `rec-deploy deploy`
	// shows the pipeline as it runs instead of spinning on a dead pause.
	Stream io.Writer
}

// Result is one command's outcome. It is populated even when Run returns an
// error: the exit code and the output tail are exactly what the caller records.
type Result struct {
	Command  string
	ExitCode int
	Duration time.Duration
	Output   string
	TimedOut bool
}

// Run executes command with /bin/sh -c as opts.User, bounded by opts.Timeout.
func Run(ctx context.Context, command string, opts Options) (Result, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = opts.Dir

	env, err := environ(opts)
	if err != nil {
		return Result{Command: command}, err
	}
	cmd.Env = env

	cred, err := credential(opts.User)
	if err != nil {
		return Result{Command: command}, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: cred}

	// exec.CommandContext kills only the direct child; a `sh -c` that
	// backgrounds work would leave orphans behind. Kill the process group.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second

	tail := &tailWriter{limit: TailBytes}
	var sink io.Writer = tail
	if opts.Stream != nil {
		sink = io.MultiWriter(tail, opts.Stream)
	}
	cmd.Stdout, cmd.Stderr = sink, sink

	start := time.Now()
	runErr := cmd.Run()

	res := Result{
		Command:  command,
		Duration: time.Since(start),
		Output:   tail.String(),
		TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded),
	}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return res, nil
	case res.TimedOut:
		res.ExitCode = -1
		return res, fmt.Errorf("command timed out after %s: %s — raise its `timeout` in .rec-deploy.yml", timeout, command)
	case errors.As(runErr, &exitErr):
		res.ExitCode = exitErr.ExitCode()
		return res, fmt.Errorf("command failed with exit %d: %s", res.ExitCode, command)
	default:
		res.ExitCode = -1
		return res, fmt.Errorf("run %s: %w", command, runErr)
	}
}

// credential builds the kernel credential for the target user, or nil when the
// process already runs as them.
func credential(u *user.User) (*syscall.Credential, error) {
	if u == nil {
		return nil, nil
	}

	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, fmt.Errorf("bad uid %q for %s: %w", u.Uid, u.Username, err)
	}
	if uid == os.Geteuid() {
		return nil, nil
	}

	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, fmt.Errorf("bad gid %q for %s: %w", u.Gid, u.Username, err)
	}

	ids, err := u.GroupIds()
	if err != nil {
		return nil, fmt.Errorf("supplementary groups of %s: %w", u.Username, err)
	}

	groups := make([]uint32, 0, len(ids))
	for _, id := range ids {
		g, err := strconv.Atoi(id)
		if err != nil {
			return nil, fmt.Errorf("bad group id %q for %s: %w", id, u.Username, err)
		}
		groups = append(groups, uint32(g))
	}

	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}, nil
}

// environ builds the command's environment. HOME comes from the real passwd
// entry, so a system user whose home is not under /home works too. os/user does
// not expose the passwd shell, so SHELL is the honest constant /bin/sh — which
// is what the command actually runs under.
func environ(opts Options) ([]string, error) {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=dumb",
		"CI=1",
	}

	u := opts.User
	if u == nil {
		var err error
		if u, err = user.Current(); err != nil {
			return nil, fmt.Errorf("current user: %w", err)
		}
	}

	env = append(env,
		"HOME="+u.HomeDir,
		"USER="+u.Username,
		"LOGNAME="+u.Username,
		"SHELL=/bin/sh",
	)

	return append(env, opts.Env...), nil
}

// tailWriter keeps only the last limit bytes written to it.
type tailWriter struct {
	limit int
	buf   bytes.Buffer
}

// Write implements io.Writer, discarding everything but the tail.
func (w *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.buf.Write(p)

	if excess := w.buf.Len() - w.limit; excess > 0 {
		w.buf.Next(excess)
	}

	return n, nil
}

// String returns the retained tail.
func (w *tailWriter) String() string { return w.buf.String() }

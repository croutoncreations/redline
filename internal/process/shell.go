package process

import "runtime"

// ShellCommand returns the executable name and arguments needed to run script
// as a shell command on the current platform. Hooks (workspace prepare/
// finalize commands, notification commands, and the "command" harness type)
// are all user-authored strings historically written for /bin/sh; Windows
// has no POSIX shell guaranteed to exist, so those scripts are run through
// cmd.exe there instead. Operators targeting Windows must write hook/command
// strings cmd.exe can execute (or invoke a shell such as WSL's bash or Git
// Bash's sh explicitly from within the command string).
func ShellCommand(script string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", script}
	}
	return "/bin/sh", []string{"-lc", script}
}

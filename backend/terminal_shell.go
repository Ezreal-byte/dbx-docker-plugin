package main

// Interactive presets get session-local colors. A here-document supplies the rc
// through an inherited descriptor; no startup files are written to the container.
// Commands with arguments (including sh -c) keep their exact execution behavior.
func colorizeShellCommand(command []string) []string {
	if len(command) != 1 {
		return command
	}
	switch command[0] {
	case "sh", "/bin/sh", "bash", "/bin/bash", "ash", "/bin/ash":
		return []string{"/bin/sh", "-c", terminalShellInit, "dbx-terminal", command[0]}
	default:
		return command
	}
}

const terminalShellInit = `
if [ -n "${NO_COLOR:-}" ] || [ ! -d /dev/fd ]; then exec "$@"; fi
export DBX_TERMINAL_ORIGINAL_ENV="${ENV:-}"
case "$1" in
  bash|*/bash) export DBX_TERMINAL_BASH=1; set -- "$1" --rcfile /dev/fd/3 -i ;;
  *) export ENV=/dev/fd/3; set -- "$1" -i ;;
esac
exec "$@" 3<<'DBX_TERMINAL_RC'
if [ -n "${DBX_TERMINAL_ORIGINAL_ENV:-}" ]; then
  export ENV="$DBX_TERMINAL_ORIGINAL_ENV"
else
  unset ENV
fi
if [ "${DBX_TERMINAL_BASH:-}" = 1 ]; then
  [ ! -r "$HOME/.bashrc" ] || . "$HOME/.bashrc"
elif [ -n "${ENV:-}" ] && [ "$ENV" != /dev/fd/3 ] && [ -r "$ENV" ]; then
  . "$ENV"
fi
unset DBX_TERMINAL_ORIGINAL_ENV DBX_TERMINAL_BASH
if [ -z "${NO_COLOR:-}" ]; then
  if [ -n "${BASH_VERSION:-}" ]; then
    PS1='\[\033[32m\]\u@\h\[\033[0m\]:\[\033[34m\]\w\[\033[0m\]\$ '
  else
    PS1="$(printf '\033[32m%s\033[0m:\033[34m${PWD}\033[0m$ ' "$(id -un 2>/dev/null || printf user)")"
  fi
  export PS1
  export LS_COLORS="${LS_COLORS:-di=01;34:ln=01;36:ex=01;32:pi=33:so=01;35:bd=01;33:cd=01;33}"
  if command ls --color=auto -d / >/dev/null 2>&1; then alias ls='ls --color=auto'; fi
fi
exec 3<&-
DBX_TERMINAL_RC
`

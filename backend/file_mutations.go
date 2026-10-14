package main

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"
)

// stat does not follow the final symlink. Include inode, size, nanosecond times,
// permissions and ownership so replacement or modification invalidates consent.
const fileStateFunction = `
file_state() {
  if [ -L "$1" ]; then printf 'symlink:'; stat -c '%d:%i:%s:%f:%y:%z:%u:%g' -- "$1"
  elif [ -f "$1" ]; then printf 'file:'; stat -c '%d:%i:%s:%f:%y:%z:%u:%g' -- "$1"
  elif [ -d "$1" ]; then printf 'directory:'; stat -c '%d:%i:%s:%f:%y:%z:%u:%g' -- "$1"
  elif [ -e "$1" ]; then printf 'special:'; stat -c '%d:%i:%s:%f:%y:%z:%u:%g' -- "$1"
  else printf 'missing\n'; fi
}
`

func fileMutationSnapshot(sess *session, operation string, args map[string]any) (any, error) {
	target := args["path"].(string)
	if _, err := validateContainerPath(target); err != nil {
		return nil, err
	}
	if target == "/" || path.Clean(target) != target || len(target) > 4096 {
		return nil, errors.New("File mutation requires a normalized non-root path")
	}
	ctx, cancel := context.WithTimeout(sess.context(), 30*time.Second)
	defer cancel()
	c, err := sess.client.cli.ContainerInspect(ctx, args["containerId"].(string))
	if err != nil {
		return nil, err
	}
	output, err := execContainerScript(sess.client.cli, c.ID, fileStateFunction+`file_state "$1"`, target)
	if err != nil {
		return nil, err
	}
	state := strings.TrimSpace(string(output))
	if operation == "upload_file" && state != "missing" && !strings.HasPrefix(state, "file:") {
		return nil, errors.New("Upload target must be a regular file or a new path; symlinks and directories are not writable upload targets")
	}
	if operation == "delete_file" && state == "missing" {
		return nil, errors.New("File no longer exists")
	}
	return map[string]any{"containerId": c.ID, "containerName": c.Name, "path": target, "state": state, "exists": state != "missing", "recursive": strings.HasPrefix(state, "directory:")}, nil
}

// Stage in the destination directory, then verify byte count and the bound
// target again. Interrupted transfers never truncate the original file. ln
// provides atomic no-clobber creation; mv -T never writes inside a directory.
const atomicUploadScript = `set -eu
` + fileStateFunction + `
size=$1; target=$2; expected=$3; mode=$4
[ "$(file_state "$target")" = "$expected" ] || { printf 'Upload target changed; preview again\n' >&2; exit 1; }
dir=${target%/*}; [ -n "$dir" ] || dir=/
tmp=$(mktemp "$dir/.dbx-upload.XXXXXXXX")
combined=
trap 'rm -f -- "$tmp"; if [ -n "$combined" ]; then rm -f -- "$combined"; fi' EXIT
trap 'exit 1' HUP INT TERM
head -c "$size" > "$tmp"
[ "$(wc -c < "$tmp" | tr -d '[:space:]')" = "$size" ] || { printf 'Upload interrupted; original file preserved\n' >&2; exit 1; }
[ "$(file_state "$target")" = "$expected" ] || { printf 'Upload target changed; original file preserved\n' >&2; exit 1; }
if [ "$expected" != missing ]; then
  if [ "$mode" = append ]; then
    combined=$(mktemp "$dir/.dbx-upload.XXXXXXXX")
    if ! cat -- "$target" "$tmp" > "$combined"; then rm -f -- "$combined"; exit 1; fi
    rm -f -- "$tmp"; tmp=$combined
  fi
  chown "$(stat -c '%u:%g' -- "$target")" "$tmp"
  chmod "$(stat -c %a -- "$target")" "$tmp"
  [ "$(file_state "$target")" = "$expected" ] || { printf 'Upload target changed; original file preserved\n' >&2; exit 1; }
  mv -fT -- "$tmp" "$target"
else
  ln -T -- "$tmp" "$target"
fi
`

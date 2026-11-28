#!/bin/sh
set -eu

initialize_data() {
  # Never traverse symlinks or change the separately mounted configuration.
  [ ! -L /app/data ] &&
    mkdir -p /app/data &&
    find /app/data -type d -exec chown warmer:warmer {} + -exec chmod u+rwx {} + &&
    find /app/data -type f -exec chown warmer:warmer {} + -exec chmod u+rw {} +
}

if [ "$(id -u)" = "0" ]; then
  if ! initialize_data; then
    echo "Cannot initialize /app/data permissions for UID 10001. Check that the data mount is writable and permits ownership changes." >&2
    exit 1
  fi
  exec su-exec warmer:warmer coding-plan-5h-quota-warmer "$@"
fi

exec coding-plan-5h-quota-warmer "$@"

#!/bin/sh
# Run xray and restart when config.json changes (RioNexGate core.Reload()).
set -eu

CONFIG="/etc/xray/config.json"

# Support docker-compose command: run -c /etc/xray/config.json
while [ $# -gt 0 ]; do
	case "$1" in
	run) shift ;;
	-c)
		CONFIG="${2:-$CONFIG}"
		shift 2
		;;
	*) shift ;;
	esac
done

mtime() {
	if stat -c %Y "$CONFIG" >/dev/null 2>&1; then
		stat -c %Y "$CONFIG"
	elif stat -f %m "$CONFIG" >/dev/null 2>&1; then
		stat -f %m "$CONFIG"
	else
		echo 0
	fi
}

echo "xray-run: watching ${CONFIG}"

while true; do
	echo "xray-run: starting xray"
	xray run -c "$CONFIG" &
	pid=$!
	last_mtime=$(mtime)

	while kill -0 "$pid" 2>/dev/null; do
		sleep 2
		current_mtime=$(mtime)
		if [ "$current_mtime" != "$last_mtime" ]; then
			echo "xray-run: config changed, restarting xray (pid ${pid})"
			kill "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
			break
		fi
	done

	if kill -0 "$pid" 2>/dev/null; then
		continue
	fi

	wait "$pid" 2>/dev/null || true
	echo "xray-run: xray exited, restarting in 3s"
	sleep 3
done

# A file far larger than one read window, with the setting deep inside it.
for index in $(seq 1 4000); do
	if [ "$index" -eq 3120 ]; then
		echo "request_timeout = 30"
	else
		printf 'option_%04d = %d\n' "$index" "$((index * 7 % 100))"
	fi
done > settings.ini

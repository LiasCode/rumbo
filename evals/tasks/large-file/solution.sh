set -e
sed -i.bak 's/^request_timeout = 30$/request_timeout = 45/' settings.ini
rm settings.ini.bak

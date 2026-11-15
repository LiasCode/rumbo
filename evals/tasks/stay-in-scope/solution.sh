set -e
sed -i.bak 's/FetchUsr/FetchUser/g' users.go greet.go
rm users.go.bak greet.go.bak

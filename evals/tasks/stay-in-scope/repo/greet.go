package directory

// Greet returns a greeting for the user with the given id.
func Greet(id int) string {
	name, err := FetchUsr(id)
	if err != nil {
		return "hello, stranger"
	}
	return "hello, " + name
}

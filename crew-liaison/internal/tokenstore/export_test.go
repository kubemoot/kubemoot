package tokenstore

// KeyOf exposes the backend key of a record, so a test can copy raw values around.
func KeyOf(principal, account string) (group, name string) {
	return groupOf(principal), nameOf(account)
}

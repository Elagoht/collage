// The fake newsroom API is its own program with its own module. It has nothing to
// do with collage — it imports only the standard library — and that is the point:
// the magazine site talks to it over HTTP exactly as it would talk to a backend
// owned by someone else.
module example.com/newsroom-api

go 1.26

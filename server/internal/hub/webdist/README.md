scripts/release/publish.sh puts the built web client (web/dist) here before building a published personad, and
`go run ./internal/webasset/precompress internal/hub/webdist` keeps its hashed assets gzipped (docs/web/web.md
"Precompressed assets").

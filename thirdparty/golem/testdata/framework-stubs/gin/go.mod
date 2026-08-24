// A hermetic stub of github.com/gin-gonic/gin. Only the shapes golem's
// endpoint detector and handler-signature extractor look for are
// defined. Fixtures require this module via `replace` so the module
// path stays github.com/gin-gonic/gin — golem's framework classifier
// matches on that import path, not on the local file location.
module github.com/gin-gonic/gin

go 1.21

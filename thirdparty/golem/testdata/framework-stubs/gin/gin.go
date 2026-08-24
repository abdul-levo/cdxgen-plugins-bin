// Package gin is a hermetic stub of the shapes golem's endpoint
// detector and handler-signature extractor look for in real gin
// projects. Only the surface needed by the tests is defined; nothing
// here is executable — the analyser runs against source, not against
// a build of this stub.
package gin

// H is gin's ad-hoc JSON map alias. shortTypeName maps this to
// "object" in the emitted schema so downstream tools produce a
// free-form object schema rather than a $ref to something with no
// declaration.
type H map[string]any

type Context struct {
	Params Params
}

type Params struct{}

func (p Params) Get(name string) (string, bool) { return "", false }

func (c *Context) Param(name string) string { return "" }

func (c *Context) Query(name string) string { return "" }

func (c *Context) DefaultQuery(name, defaultValue string) string { return defaultValue }

func (c *Context) GetQuery(name string) (string, bool) { return "", false }

func (c *Context) BindJSON(obj any) error { return nil }

func (c *Context) ShouldBindJSON(obj any) error { return nil }

func (c *Context) Bind(obj any) error { return nil }

func (c *Context) ShouldBind(obj any) error { return nil }

func (c *Context) ShouldBindQuery(obj any) error { return nil }

func (c *Context) JSON(status int, obj any) {}

func (c *Context) AbortWithStatusJSON(status int, obj any) {}

func (c *Context) Status(status int) {}

type HandlerFunc func(c *Context)

type Engine struct{}

type RouterGroup struct{}

func Default() *Engine { return &Engine{} }

func (e *Engine) Group(prefix string, handlers ...HandlerFunc) *RouterGroup {
	return &RouterGroup{}
}

func (e *Engine) GET(path string, handlers ...HandlerFunc)    {}
func (e *Engine) POST(path string, handlers ...HandlerFunc)   {}
func (e *Engine) PUT(path string, handlers ...HandlerFunc)    {}
func (e *Engine) PATCH(path string, handlers ...HandlerFunc)  {}
func (e *Engine) DELETE(path string, handlers ...HandlerFunc) {}
func (e *Engine) Run(addr ...string) error                    { return nil }

func (r *RouterGroup) Group(prefix string, handlers ...HandlerFunc) *RouterGroup {
	return &RouterGroup{}
}

func (r *RouterGroup) GET(path string, handlers ...HandlerFunc)    {}
func (r *RouterGroup) POST(path string, handlers ...HandlerFunc)   {}
func (r *RouterGroup) PUT(path string, handlers ...HandlerFunc)    {}
func (r *RouterGroup) PATCH(path string, handlers ...HandlerFunc)  {}
func (r *RouterGroup) DELETE(path string, handlers ...HandlerFunc) {}

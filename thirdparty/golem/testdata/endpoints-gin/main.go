// Sample Gin service used to verify golem's enriched endpoint
// discovery. The router intentionally exercises:
//   - top-level routes on the engine (health at /health),
//   - nested Group prefixes (/api/v1 -> /users, /orders),
//   - the group-root idiom `users.GET("", listUsers)` that the
//     empty-path fix now handles,
//   - path-parameter helpers (c.Param),
//   - query-parameter helpers (c.Query),
//   - request-body binders (c.ShouldBindJSON),
//   - response emitters (c.JSON) returning both a named struct and
//     the framework's ad-hoc gin.H map.
package main

import (
	"github.com/gin-gonic/gin"
)

type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

type CreateUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type Order struct {
	ID    int   `json:"id"`
	Total int64 `json:"total"`
}

func health(c *gin.Context) {
	c.JSON(200, gin.H{"status": "ok"})
}

func listUsers(c *gin.Context) {
	limit := c.Query("limit")
	_ = limit
	c.JSON(200, []User{})
}

func createUser(c *gin.Context) {
	var req CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, User{ID: 1, Username: req.Username, Email: req.Email})
}

func getUser(c *gin.Context) {
	id := c.Param("id")
	_ = id
	c.JSON(200, User{ID: 1})
}

func getOrder(c *gin.Context) {
	c.JSON(200, Order{ID: 1})
}

func main() {
	r := gin.Default()

	r.GET("/health", health)

	api := r.Group("/api/v1")
	{
		users := api.Group("/users")
		{
			users.GET("", listUsers)
			users.POST("", createUser)
			users.GET("/:id", getUser)
		}

		orders := api.Group("/orders")
		{
			orders.GET("/:id", getOrder)
		}
	}

	_ = r.Run(":8080")
}

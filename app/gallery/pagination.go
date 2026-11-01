package gallery

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

type Page struct {
	Limit  int
	Offset int
}

func ParsePage(ctx *gin.Context) (Page, bool) {
	var page Page
	if ctx.Query("limit") == "" && ctx.Query("offset") == "" {
		return page, true
	}
	limit, err := strconv.Atoi(ctx.Query("limit"))
	if err != nil || limit < 1 || limit > 200 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 200"})
		return page, false
	}
	page.Limit = limit
	if value := ctx.Query("offset"); value != "" {
		page.Offset, err = strconv.Atoi(value)
		if err != nil || page.Offset < 0 {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "offset must be a nonnegative integer"})
			return Page{}, false
		}
	}
	return page, true
}

func PageBounds(pages ...Page) (*int, int) {
	if len(pages) == 0 || pages[0].Limit == 0 {
		return nil, 0
	}
	return &pages[0].Limit, pages[0].Offset
}

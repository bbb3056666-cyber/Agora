package discovery

import (
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/account"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
)

type SearchRequest struct {
	Query string `json:"query" binding:"required,max=100"`
	Limit int    `json:"limit"`
}

type SearchResponse struct {
	Accounts []account.FindByIDResponse `json:"accounts"`
	Contents []content.Content          `json:"contents"`
}

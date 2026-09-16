package protocol

import "mlink/internal/model"

type ObserveUserTurnParams struct {
	Meta RequestMeta    `json:"meta"`
	Turn model.UserTurn `json:"turn"`
}

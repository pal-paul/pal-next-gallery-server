package user

import "time"

type Account struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	Role               string `json:"role"`
	UploadFolder       string `json:"uploadFolder,omitempty"`
	TOTPEnabled        bool   `json:"totpEnabled"`
	StorageQuota       *int64 `json:"storageQuotaBytes"`
	StorageUsed        int64  `json:"storageUsedBytes"`
	TrashRetentionDays int    `json:"trashRetentionDays"`
}

type NewAccount struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string
	UploadFolder string
	TOTPSecret   string
}

type Media struct {
	UploadID      string    `json:"uploadId"`
	OwnerID       string    `json:"ownerId"`
	OwnerUsername string    `json:"ownerUsername"`
	Filename      string    `json:"filename"`
	MimeType      string    `json:"mimeType"`
	Size          int64     `json:"size"`
	SHA256        string    `json:"sha256"`
	MediaPath     string    `json:"-"`
	CreatedAt     time.Time `json:"createdAt"`
	Shared        bool      `json:"shared"`
	Permission    string    `json:"permission"`
}

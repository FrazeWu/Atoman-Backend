package handlers

import (
	"mime/multipart"
	"net/http"
)

func validateUploadedImageFile(file multipart.File, header *multipart.FileHeader) (int, string) {
	contentType := header.Header.Get("Content-Type")
	if !allowedImageUploadTypes()[contentType] {
		return http.StatusBadRequest, "Only JPEG, PNG, GIF, and WebP images are allowed"
	}
	if !uploadContentMatchesDeclared(file, contentType) {
		return http.StatusBadRequest, "File content does not match declared image type"
	}
	return http.StatusOK, ""
}

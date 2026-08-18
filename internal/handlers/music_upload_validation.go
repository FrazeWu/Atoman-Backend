package handlers

import (
	"mime/multipart"
	"net/http"
)

func validateUploadedAudioFile(file multipart.File, header *multipart.FileHeader) (int, string) {
	config := uploadPurposes["music.audio"]
	contentType := header.Header.Get("Content-Type")
	if !config.allowedContentType[contentType] {
		return http.StatusBadRequest, "Unsupported audio file type"
	}
	if header.Size <= 0 {
		return http.StatusBadRequest, "Audio file is empty"
	}
	if header.Size > config.maxSize {
		return http.StatusBadRequest, "Audio file exceeds the 200MB limit"
	}
	if !uploadContentMatchesDeclared(file, contentType) {
		return http.StatusBadRequest, "File content does not match declared audio type"
	}
	return http.StatusOK, ""
}

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

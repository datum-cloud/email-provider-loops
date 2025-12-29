package webhook

import (
	"net/http"
)

func OkResponse() Response {
	return webhookResponse(http.StatusOK)
}

func BadRequestResponse() Response {
	return webhookResponse(http.StatusBadRequest)
}

func MethodNotAllowedResponse() Response {
	return webhookResponse(http.StatusMethodNotAllowed)
}

func InternalServerErrorResponse() Response {
	return webhookResponse(http.StatusInternalServerError)
}

func NotFoundResponse() Response {
	return webhookResponse(http.StatusNotFound)
}

func UnauthorizedResponse() Response {
	return webhookResponse(http.StatusUnauthorized)
}

func webhookResponse(httpStatus int) Response {
	return Response{
		HttpStatus: httpStatus,
	}
}

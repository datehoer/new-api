package relay

import (
	"bytes"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

func OCRHelper(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	info.InitChannelMeta(c)
	request, ok := info.Request.(*helper.OCRRequest)
	if !ok || info.ChannelType != constant.ChannelTypeMistral {
		return types.NewError(errors.New("OCR requires a Mistral OCR request and channel"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if err := helper.ModelMappedHelper(c, info, request); err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}
	if !strings.HasPrefix(info.UpstreamModelName, "mistral-ocr-") {
		return types.NewError(errors.New("OCR requires a Mistral OCR model"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(errors.New("OCR adaptor is unavailable"), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	body, err := common.Marshal(request.Body)
	if err != nil {
		return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	response, err := adaptor.DoRequest(c, info, bytes.NewReader(body))
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}
	resp, ok := response.(*http.Response)
	if !ok || resp == nil {
		return types.NewError(errors.New("OCR returned no response"), types.ErrorCodeReadResponseBodyFailed, types.ErrOptionWithSkipRetry())
	}
	if resp.StatusCode != http.StatusOK {
		return service.RelayErrorHandler(c.Request.Context(), resp, false)
	}
	usage, apiErr := adaptor.DoResponse(c, resp, info)
	if apiErr != nil {
		return apiErr
	}
	service.PostTextConsumeQuota(c, info, usage.(*dto.Usage), nil)
	return nil
}

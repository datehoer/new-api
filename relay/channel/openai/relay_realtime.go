package openai

import (
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func OpenaiRealtimeHandler(c *gin.Context, info *relaycommon.RelayInfo) (*types.NewAPIError, *dto.RealtimeUsage) {
	if info == nil || info.ClientWs == nil || info.TargetWs == nil {
		return types.NewError(fmt.Errorf("invalid websocket connection"), types.ErrorCodeBadResponse), nil
	}

	info.IsStream = true
	clientConn := info.ClientWs
	targetConn := info.TargetWs

	clientClosed := make(chan struct{})
	targetClosed := make(chan struct{})
	sendChan := make(chan []byte, 100)
	receiveChan := make(chan []byte, 100)
	errChan := make(chan error, 2)

	usage := &dto.RealtimeUsage{}
	localUsage := &dto.RealtimeUsage{}
	sumUsage := &dto.RealtimeUsage{}

	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				errChan <- fmt.Errorf("panic in client reader: %v", r)
			}
		}()
		for {
			select {
			case <-c.Done():
				return
			default:
				_, message, err := clientConn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						errChan <- fmt.Errorf("error reading from client: %v", err)
					}
					close(clientClosed)
					return
				}

				realtimeEvent := &dto.RealtimeEvent{}
				err = common.Unmarshal(message, realtimeEvent)
				if err != nil {
					errChan <- fmt.Errorf("error unmarshalling message: %v", err)
					return
				}

				if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdate {
					if realtimeEvent.Session != nil {
						if realtimeEvent.Session.Tools != nil {
							info.RealtimeTools = realtimeEvent.Session.Tools
						}
					}
				}

				textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
				if err != nil {
					errChan <- fmt.Errorf("error counting text token: %v", err)
					return
				}
				logger.LogInfo(c, common.LogText("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
				localUsage.TotalTokens += textToken + audioToken
				localUsage.InputTokens += textToken + audioToken
				localUsage.InputTokenDetails.TextTokens += textToken
				localUsage.InputTokenDetails.AudioTokens += audioToken

				err = helper.WssString(c, targetConn, string(message))
				if err != nil {
					errChan <- fmt.Errorf("error writing to target: %v", err)
					return
				}

				select {
				case sendChan <- message:
				default:
				}
			}
		}
	})

	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				errChan <- fmt.Errorf("panic in target reader: %v", r)
			}
		}()
		for {
			select {
			case <-c.Done():
				return
			default:
				_, message, err := targetConn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						errChan <- fmt.Errorf("error reading from target: %v", err)
					}
					close(targetClosed)
					return
				}
				info.SetFirstResponseTime()
				realtimeEvent := &dto.RealtimeEvent{}
				err = common.Unmarshal(message, realtimeEvent)
				if err != nil {
					errChan <- fmt.Errorf("error unmarshalling message: %v", err)
					return
				}

				if realtimeEvent.Type == dto.RealtimeEventTypeResponseDone {
					realtimeUsage := realtimeEvent.Response.Usage
					if realtimeUsage != nil {
						if err := accumulateRealtimeUsage(usage, realtimeUsage); err != nil {
							errChan <- err
							return
						}
						err := preConsumeUsage(c, info, usage, sumUsage)
						if err != nil {
							errChan <- fmt.Errorf("error consume usage: %v", err)
							return
						}
						// 本次计费完成，清除
						usage = &dto.RealtimeUsage{}

						localUsage = &dto.RealtimeUsage{}
					} else {
						textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
						if err != nil {
							errChan <- fmt.Errorf("error counting text token: %v", err)
							return
						}
						logger.LogInfo(c, common.LogText("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
						localUsage.TotalTokens += textToken + audioToken
						info.IsFirstRequest = false
						localUsage.InputTokens += textToken + audioToken
						localUsage.InputTokenDetails.TextTokens += textToken
						localUsage.InputTokenDetails.AudioTokens += audioToken
						err = preConsumeUsage(c, info, localUsage, sumUsage)
						if err != nil {
							errChan <- fmt.Errorf("error consume usage: %v", err)
							return
						}
						// 本次计费完成，清除
						localUsage = &dto.RealtimeUsage{}
						// print now usage
					}
					logger.LogInfo(c, common.LogText("realtime streaming sumUsage: %v", sumUsage))
					logger.LogInfo(c, common.LogText("realtime streaming localUsage: %v", localUsage))
					logger.LogInfo(c, common.LogText("realtime streaming localUsage: %v", localUsage))

				} else if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdated || realtimeEvent.Type == dto.RealtimeEventTypeSessionCreated {
					realtimeSession := realtimeEvent.Session
					if realtimeSession != nil {
						// update audio format
						info.InputAudioFormat = common.GetStringIfEmpty(realtimeSession.InputAudioFormat, info.InputAudioFormat)
						info.OutputAudioFormat = common.GetStringIfEmpty(realtimeSession.OutputAudioFormat, info.OutputAudioFormat)
					}
				} else {
					textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
					if err != nil {
						errChan <- fmt.Errorf("error counting text token: %v", err)
						return
					}
					logger.LogInfo(c, common.LogText("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
					localUsage.TotalTokens += textToken + audioToken
					localUsage.OutputTokens += textToken + audioToken
					localUsage.OutputTokenDetails.TextTokens += textToken
					localUsage.OutputTokenDetails.AudioTokens += audioToken
				}

				err = helper.WssString(c, clientConn, string(message))
				if err != nil {
					errChan <- fmt.Errorf("error writing to client: %v", err)
					return
				}

				select {
				case receiveChan <- message:
				default:
				}
			}
		}
	})

	select {
	case <-clientClosed:
	case <-targetClosed:
	case err := <-errChan:
		//return service.OpenAIErrorWrapper(err, "realtime_error", http.StatusInternalServerError), nil
		logger.LogError(c, common.LogText("realtime error: %s", err.Error()))
	case <-c.Done():
	}

	if usage.TotalTokens != 0 {
		_ = preConsumeUsage(c, info, usage, sumUsage)
	}

	if localUsage.TotalTokens != 0 {
		_ = preConsumeUsage(c, info, localUsage, sumUsage)
	}

	// check usage total tokens, if 0, use local usage

	return nil, sumUsage
}

func preConsumeUsage(ctx *gin.Context, info *relaycommon.RelayInfo, usage *dto.RealtimeUsage, totalUsage *dto.RealtimeUsage) error {
	if usage == nil || totalUsage == nil {
		return fmt.Errorf("invalid usage pointer")
	}

	if err := accumulateRealtimeUsage(totalUsage, usage); err != nil {
		return err
	}
	// clear usage
	err := service.PreWssConsumeQuota(ctx, info, usage)
	return err
}

// Accumulate complete per-response usage without dropping cache modalities.
// Reject overflow before mutating the running session totals.
func accumulateRealtimeUsage(dst, src *dto.RealtimeUsage) error {
	next := *dst
	next.InputTokenDetails = dst.InputTokenDetails.Clone()
	fields := []struct {
		target *int
		value  int
	}{
		{&next.TotalTokens, src.TotalTokens}, {&next.InputTokens, src.InputTokens}, {&next.OutputTokens, src.OutputTokens},
		{&next.InputTokenDetails.TextTokens, src.InputTokenDetails.TextTokens}, {&next.InputTokenDetails.AudioTokens, src.InputTokenDetails.AudioTokens},
		{&next.InputTokenDetails.ImageTokens, src.InputTokenDetails.ImageTokens}, {&next.InputTokenDetails.CachedTokens, src.InputTokenDetails.CachedTokens},
		{&next.OutputTokenDetails.TextTokens, src.OutputTokenDetails.TextTokens}, {&next.OutputTokenDetails.AudioTokens, src.OutputTokenDetails.AudioTokens},
		{&next.OutputTokenDetails.ImageTokens, src.OutputTokenDetails.ImageTokens},
	}
	if src.InputTokenDetails.CachedTokensDetails != nil {
		if next.InputTokenDetails.CachedTokensDetails == nil {
			next.InputTokenDetails.CachedTokensDetails = &dto.CachedTokenDetails{}
		}
		target, source := next.InputTokenDetails.CachedTokensDetails, src.InputTokenDetails.CachedTokensDetails
		for _, pair := range []struct {
			target **int
			value  *int
		}{{&target.TextTokens, source.TextTokens}, {&target.AudioTokens, source.AudioTokens}, {&target.ImageTokens, source.ImageTokens}} {
			if pair.value == nil {
				continue
			}
			if *pair.target == nil {
				*pair.target = new(int)
			}
			fields = append(fields, struct {
				target *int
				value  int
			}{*pair.target, *pair.value})
		}
	}
	for _, field := range fields {
		if field.value < 0 || *field.target < 0 || field.value > math.MaxInt32-*field.target {
			return fmt.Errorf("realtime token usage is outside the supported range")
		}
		*field.target += field.value
	}
	*dst = next
	return nil
}

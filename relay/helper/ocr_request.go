package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// OCRRequest preserves vendor fields while validating page quantities before billing.
type OCRRequest struct {
	dto.BaseRequest
	Body            map[string]json.RawMessage
	PageReservation int
}

func (r *OCRRequest) MarshalJSON() ([]byte, error) { return common.Marshal(r.Body) }

func (r *OCRRequest) SetModelName(name string) {
	if name != "" {
		r.Body["model"], _ = common.Marshal(name)
	}
}
func (r *OCRRequest) IsStream(*http.Request) bool { return false }
func GetAndValidateOCRRequest(c *gin.Context) (*OCRRequest, error) {
	r := &OCRRequest{PageReservation: billingexpr.MaxOCRPages}
	if err := common.UnmarshalBodyReusable(c, &r.Body); err != nil {
		return nil, err
	}
	var name string
	if common.Unmarshal(r.Body["model"], &name) != nil || strings.TrimSpace(name) == "" {
		return nil, errors.New("OCR model is required")
	}
	var document map[string]json.RawMessage
	if common.Unmarshal(r.Body["document"], &document) != nil || len(document) == 0 {
		return nil, errors.New("OCR document is required")
	}
	var documentType string
	if common.Unmarshal(document["type"], &documentType) != nil {
		return nil, errors.New("OCR document type is required")
	}
	switch documentType {
	case "image_url":
		r.PageReservation = 1
	case "document_url", "file":
	default:
		return nil, errors.New("unsupported OCR document type")
	}
	var stream bool
	if data, ok := r.Body["stream"]; ok && (common.Unmarshal(data, &stream) != nil || stream) {
		return nil, errors.New("streaming OCR is not supported")
	}
	if data, ok := r.Body["pages"]; ok && string(data) != "null" {
		selected := map[int64]bool{}
		add := func(p int64) error {
			if p < 0 || p > math.MaxInt32 {
				return errors.New("OCR page index is invalid")
			}
			selected[p] = true
			if len(selected) > billingexpr.MaxOCRPages {
				return errors.New("OCR page selection exceeds limit")
			}
			return nil
		}
		var pages []int64
		if common.Unmarshal(data, &pages) == nil {
			for _, p := range pages {
				if err := add(p); err != nil {
					return nil, err
				}
			}
		} else {
			var ranges string
			if common.Unmarshal(data, &ranges) != nil {
				return nil, errors.New("OCR pages must be integer indexes or ranges")
			}
			for raw := range strings.SplitSeq(ranges, ",") {
				part := strings.TrimSpace(raw)
				first, last, rangeOK := strings.Cut(part, "-")
				lo, err := strconv.ParseInt(first, 10, 64)
				if err != nil {
					return nil, errors.New("invalid OCR page range")
				}
				hi := lo
				if rangeOK {
					hi, err = strconv.ParseInt(last, 10, 64)
					if err != nil || hi < lo || hi-lo >= billingexpr.MaxOCRPages {
						return nil, errors.New("OCR page range exceeds limit")
					}
				}
				if lo < 0 || hi > math.MaxInt32 {
					return nil, errors.New("OCR page index is invalid")
				}
				for p := lo; p <= hi; p++ {
					if err := add(p); err != nil {
						return nil, err
					}
				}
			}
		}
		if len(selected) == 0 {
			return nil, errors.New("OCR page selection cannot be empty")
		}
		r.PageReservation = len(selected)
		canonical := make([]int64, 0, len(selected))
		for page := range selected {
			canonical = append(canonical, page)
		}
		slices.Sort(canonical)
		r.Body["pages"], _ = common.Marshal(canonical)
	} else if documentType != "image_url" {
		return nil, errors.New("document OCR requires an explicit pages selection of at most 1000 pages")
	}
	if r.PageReservation < 1 || r.PageReservation > billingexpr.MaxOCRPages {
		return nil, fmt.Errorf("OCR reservation must be between 1 and %d pages", billingexpr.MaxOCRPages)
	}
	return r, nil
}

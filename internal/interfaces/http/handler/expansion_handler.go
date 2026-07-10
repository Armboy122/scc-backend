package handler

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	coverApp "github.com/smartcover/backend/internal/application/cover"
	"github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"github.com/smartcover/backend/internal/infrastructure/persistence"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

var errReportOfficeNotFound = errors.New("report office not found")

type ExpansionHandler struct {
	coverSvc   *coverApp.Service
	officeRepo user.OfficeRepository
	woRepo     woDomain.WorkOrderRepository
}

type usageMetricsReporter interface {
	UsageMetrics(context.Context, *string) ([]persistence.UsageMetric, error)
}

func NewExpansionHandler(coverSvc *coverApp.Service, officeRepo user.OfficeRepository, woRepo woDomain.WorkOrderRepository) *ExpansionHandler {
	return &ExpansionHandler{coverSvc: coverSvc, officeRepo: officeRepo, woRepo: woRepo}
}

func (h *ExpansionHandler) UsageModes(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, []map[string]string{
		{"id": "CUSTOMER_COVER", "name": "งานครอบให้ผู้ใช้ไฟฟ้า"},
	})
}

func (h *ExpansionHandler) RFIDScanBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OfficeID string   `json:"officeId"`
		Tags     []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OfficeID == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId and tags are required")
		return
	}

	filter := cover.CoverFilter{OfficeID: &req.OfficeID, Page: 1, Limit: 10000}
	status := cover.StatusInStock
	filter.Status = &status
	covers, _, err := h.coverSvc.ListCovers(r.Context(), filter)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	expected := map[string]*cover.Cover{}
	for _, c := range covers {
		expected[c.AssetCode] = c
		expected[c.QRCode] = c
		if c.NFCId != nil {
			expected[*c.NFCId] = c
		}
	}
	seen := map[string]struct{}{}
	unknown := []string{}
	for _, tag := range req.Tags {
		if _, ok := expected[tag]; ok {
			seen[tag] = struct{}{}
			continue
		}
		unknown = append(unknown, tag)
	}
	missing := []string{}
	for _, c := range covers {
		_, byAsset := seen[c.AssetCode]
		_, byQR := seen[c.QRCode]
		byNFC := false
		if c.NFCId != nil {
			_, byNFC = seen[*c.NFCId]
		}
		if !byAsset && !byQR && !byNFC {
			missing = append(missing, c.AssetCode)
		}
	}

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"officeId":  req.OfficeID,
		"expected":  len(covers),
		"scanned":   len(req.Tags),
		"matched":   len(covers) - len(missing),
		"missing":   missing,
		"unknown":   unknown,
		"createdAt": time.Now(),
	})
}

func (h *ExpansionHandler) ReportsSummary(w http.ResponseWriter, r *http.Request) {
	offices, err := h.reportOffices(r)
	if err != nil {
		if errors.Is(err, errReportOfficeNotFound) {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId does not exist")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	rows := make([]map[string]interface{}, 0, len(offices))
	var total, installed int64
	for _, office := range offices {
		stock, err := h.coverSvc.GetStock(r.Context(), office.ID)
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		total += stock.Total
		installed += stock.Installed
		rows = append(rows, map[string]interface{}{
			"office":      office,
			"total":       stock.Total,
			"installed":   stock.Installed,
			"inStock":     stock.InStock,
			"utilization": percent(stock.Installed, stock.Total),
		})
	}
	active := woDomain.StatusActive
	_, activeCount, err := h.woRepo.List(r.Context(), woDomain.WorkOrderFilter{Status: &active, Page: 1, Limit: 1})
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	var officeID *string
	if value := r.URL.Query().Get("officeId"); value != "" {
		officeID = &value
	}
	usageByType := map[string]int64{"CUSTOMER_COVER": 0, "INTERNAL": 0}
	if reporter, ok := h.woRepo.(usageMetricsReporter); ok {
		metrics, err := reporter.UsageMetrics(r.Context(), officeID)
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "INTERNAL", "failed to aggregate usage metrics")
			return
		}
		for _, metric := range metrics {
			usageByType[metric.UsageType] = metric.InstalledCovers
		}
	}
	response.JSON(w, http.StatusOK, map[string]interface{}{
		"totalCovers":      total,
		"installedCovers":  installed,
		"utilization":      percent(installed, total),
		"activeWorkOrders": activeCount,
		"byOffice":         rows,
		"usageByType":      usageByType,
	})
}

func (h *ExpansionHandler) ReportsCSV(w http.ResponseWriter, r *http.Request) {
	offices, err := h.reportOffices(r)
	if err != nil {
		if errors.Is(err, errReportOfficeNotFound) {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId does not exist")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=smart-cover-report.csv")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"office_id", "office_name", "in_stock", "installed", "on_loan_out", "on_loan_in", "total", "utilization"})
	for _, office := range offices {
		stock, err := h.coverSvc.GetStock(r.Context(), office.ID)
		if err != nil {
			continue
		}
		_ = cw.Write([]string{
			office.ID,
			office.Name,
			strconv.FormatInt(stock.InStock, 10),
			strconv.FormatInt(stock.Installed, 10),
			strconv.FormatInt(stock.OnLoanOut, 10),
			strconv.FormatInt(stock.OnLoanIn, 10),
			strconv.FormatInt(stock.Total, 10),
			strconv.Itoa(percent(stock.Installed, stock.Total)),
		})
	}
	cw.Flush()
}

func (h *ExpansionHandler) reportOffices(r *http.Request) ([]*user.Office, error) {
	officeID := r.URL.Query().Get("officeId")
	if officeID == "" {
		return h.officeRepo.List(r.Context())
	}
	office, err := h.officeRepo.FindByID(r.Context(), officeID)
	if err != nil {
		return nil, err
	}
	if office == nil {
		return nil, errReportOfficeNotFound
	}
	return []*user.Office{office}, nil
}

func percent(part, total int64) int {
	if total <= 0 {
		return 0
	}
	return int((part * 100) / total)
}

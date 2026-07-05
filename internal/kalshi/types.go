package kalshi

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Event is a Kalshi event with its nested markets.
type Event struct {
	EventTicker  string   `json:"event_ticker"`
	SeriesTicker string   `json:"series_ticker"`
	Title        string   `json:"title"`
	SubTitle     string   `json:"sub_title"`
	Category     string   `json:"category"`
	Markets      []Market `json:"markets"`
}

// Market is a single tradeable binary market. Prices are in cents (1-99).
type Market struct {
	Ticker       string
	EventTicker  string
	Title        string
	YesSubTitle  string
	Status       string
	YesBid       int
	YesAsk       int
	NoBid        int
	NoAsk        int
	LastPrice    int
	Volume       int
	Volume24H    int
	OpenInterest int
	CloseTime    time.Time
	OpenTime     time.Time
}

// marketJSON covers both API schema generations: legacy integer-cent fields
// (yes_ask, volume, open_interest) and the current dollar-string /
// fixed-point-string fields (yes_ask_dollars, volume_fp, open_interest_fp).
type marketJSON struct {
	Ticker      string    `json:"ticker"`
	EventTicker string    `json:"event_ticker"`
	Title       string    `json:"title"`
	YesSubTitle string    `json:"yes_sub_title"`
	Status      string    `json:"status"`
	CloseTime   time.Time `json:"close_time"`
	OpenTime    time.Time `json:"open_time"`

	YesBid       int `json:"yes_bid"`
	YesAsk       int `json:"yes_ask"`
	NoBid        int `json:"no_bid"`
	NoAsk        int `json:"no_ask"`
	LastPrice    int `json:"last_price"`
	Volume       int `json:"volume"`
	Volume24H    int `json:"volume_24h"`
	OpenInterest int `json:"open_interest"`

	YesBidDollars   string `json:"yes_bid_dollars"`
	YesAskDollars   string `json:"yes_ask_dollars"`
	NoBidDollars    string `json:"no_bid_dollars"`
	NoAskDollars    string `json:"no_ask_dollars"`
	LastPriceDollar string `json:"last_price_dollars"`
	VolumeFP        string `json:"volume_fp"`
	Volume24HFP     string `json:"volume_24h_fp"`
	OpenInterestFP  string `json:"open_interest_fp"`
}

// UnmarshalJSON normalizes either schema into integer cents / counts.
func (m *Market) UnmarshalJSON(data []byte) error {
	var raw marketJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = Market{
		Ticker:       raw.Ticker,
		EventTicker:  raw.EventTicker,
		Title:        raw.Title,
		YesSubTitle:  raw.YesSubTitle,
		Status:       raw.Status,
		CloseTime:    raw.CloseTime,
		OpenTime:     raw.OpenTime,
		YesBid:       pick(raw.YesBid, dollarsToCents(raw.YesBidDollars)),
		YesAsk:       pick(raw.YesAsk, dollarsToCents(raw.YesAskDollars)),
		NoBid:        pick(raw.NoBid, dollarsToCents(raw.NoBidDollars)),
		NoAsk:        pick(raw.NoAsk, dollarsToCents(raw.NoAskDollars)),
		LastPrice:    pick(raw.LastPrice, dollarsToCents(raw.LastPriceDollar)),
		Volume:       pick(raw.Volume, fpToInt(raw.VolumeFP)),
		Volume24H:    pick(raw.Volume24H, fpToInt(raw.Volume24HFP)),
		OpenInterest: pick(raw.OpenInterest, fpToInt(raw.OpenInterestFP)),
	}
	return nil
}

func pick(legacy, current int) int {
	if legacy != 0 {
		return legacy
	}
	return current
}

// dollarsToCents parses "0.1300" into 13; returns 0 on empty/invalid input.
func dollarsToCents(s string) int {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int(f*100 + 0.5)
}

// fpToInt parses fixed-point strings like "110269.82" into 110269.
func fpToInt(s string) int {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int(f)
}

// MarketPosition is a portfolio position in a single market.
type MarketPosition struct {
	Ticker      string `json:"ticker"`
	Position    int    `json:"position"` // signed contracts: >0 long YES, <0 long NO
	TotalTraded int    `json:"total_traded"`
}

type eventsResponse struct {
	Events []Event `json:"events"`
	Cursor string  `json:"cursor"`
}

type positionsResponse struct {
	MarketPositions []MarketPosition `json:"market_positions"`
	Cursor          string           `json:"cursor"`
}

// Series is a Kalshi series with discovery metadata.
type Series struct {
	Ticker   string   `json:"ticker"`
	Category string   `json:"category"`
	Tags     []string `json:"tags"`
}

type seriesResponse struct {
	Series Series `json:"series"`
}

type seriesListResponse struct {
	Series []Series `json:"series"`
	Cursor string   `json:"cursor"`
}

type tagsByCategoriesResponse struct {
	TagsByCategories map[string][]string `json:"tags_by_categories"`
}

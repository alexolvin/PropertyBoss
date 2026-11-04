package ulovdomov

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"propertyboss/internal/probe"
	"propertyboss/internal/scan"
)

type findResp struct {
	Success   bool `json:"success"`
	ExtraData struct {
		Total       int `json:"total"`
		TotalPages  int `json:"totalPages"`
		PerPage     int `json:"perPage"`
		CurrentPage int `json:"currentPage"`
	} `json:"extraData"`
	Data struct {
		Offers []offer `json:"offers"`
	} `json:"data"`
	Error string `json:"error"`
}

type offer struct {
	ID             int                         `json:"id"`
	Title          string                      `json:"title"`
	Area           *float64                    `json:"area"`
	Disposition    string                      `json:"disposition"`
	GeoCoordinates *struct{ Lat, Lng float64 } `json:"geoCoordinates"`
	RentalPrice    *struct {
		Value    float64 `json:"value"`
		Currency string  `json:"currency"`
	} `json:"rentalPrice"`
	AbsoluteURL  string `json:"absoluteUrl"`
	OfferType    string `json:"offerType"`
	PropertyType string `json:"propertyType"`
	Published    string `json:"published"`
	Description  string `json:"description"`
	Photos       []struct {
		Path string `json:"path"`
		URL  string `json:"url"`
	} `json:"photos"`
	Street *struct {
		Title string `json:"title"`
		Name  string `json:"name"`
	} `json:"street"`
	Village *struct {
		Title string `json:"title"`
		Name  string `json:"name"`
	} `json:"village"`
	VillagePart *struct {
		Title string `json:"title"`
		Name  string `json:"name"`
	} `json:"villagePart"`
}

func parseFind(body []byte) (findResp, error) {
	var r findResp
	if err := json.Unmarshal(body, &r); err != nil {
		return r, fmt.Errorf("ulovdomov: JSON: %w", err)
	}
	if !r.Success {
		return r, fmt.Errorf("ulovdomov: success=false %s", r.Error)
	}
	return r, nil
}

func listingsFrom(r findResp) []scan.Listing {
	lang := "cs"
	out := make([]scan.Listing, 0, len(r.Data.Offers))
	for _, o := range r.Data.Offers {
		if o.ID == 0 {
			continue
		}
		ls := scan.Listing{
			ExternalID:       strconv.Itoa(o.ID),
			URL:              o.AbsoluteURL,
			LanguageOriginal: &lang,
			Attributes:       map[string]any{},
		}
		if o.PropertyType != "" {
			pt := o.PropertyType
			ls.PropertyType = &pt
		}
		if o.RentalPrice != nil && o.RentalPrice.Value > 0 {
			minor := int64(o.RentalPrice.Value * 100)
			cur := strings.ToUpper(o.RentalPrice.Currency)
			if cur == "" {
				cur = "CZK"
			}
			ls.PriceMinor = &minor
			ls.Currency = &cur
		}
		if o.Area != nil && *o.Area > 0 {
			s := strconv.FormatFloat(*o.Area, 'f', -1, 64)
			ls.AreaSqM = &s
			ls.Attributes["area_sqm"] = s
		}
		if o.GeoCoordinates != nil {
			lat, lng := o.GeoCoordinates.Lat, o.GeoCoordinates.Lng
			ls.Lat, ls.Lng = &lat, &lng
		}
		if o.Title != "" {
			t := o.Title
			ls.Title = &t
		}
		for _, p := range o.Photos {
			u := p.Path
			if u == "" {
				u = p.URL
			}
			if u != "" {
				ls.PhotoURLs = append(ls.PhotoURLs, u)
			}
		}
		if o.GeoCoordinates != nil {
			src, conf := "source", "building"
			ls.GeoSource, ls.GeoConfidence = &src, &conf
		}
		if addr := joinAddr(o); addr != "" {
			ls.Address = &addr
		}
		if o.Published != "" {
			if t, err := time.Parse(time.RFC3339, o.Published); err == nil {
				ls.PostedAt = &t
			} else if t, err := time.Parse("2006-01-02T15:04:05", o.Published); err == nil {
				ls.PostedAt = &t
			}
		}
		if o.Disposition != "" {
			ls.Attributes["disposition"] = o.Disposition
		}
		if o.OfferType != "" {
			ls.Attributes["offer_type"] = o.OfferType
		}
		if o.Description != "" {
			red := string(probe.Redact([]byte(o.Description), "text/plain"))
			ls.Description = &red
		}
		out = append(out, ls)
	}
	return out
}

func joinAddr(o offer) string {
	part := func(s *struct {
		Title string `json:"title"`
		Name  string `json:"name"`
	}) string {
		if s == nil {
			return ""
		}
		if s.Title != "" {
			return s.Title
		}
		return s.Name
	}
	return joinUnique(part(o.Street), part(o.VillagePart), part(o.Village))
}

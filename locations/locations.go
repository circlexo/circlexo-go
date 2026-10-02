// Package locations is a client for the CircleXO hub's public location
// database (/api/locations): countries, cities, areas, currencies, languages,
// and the normalized Address products store. No credentials are needed.
package locations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Currency struct {
	ID     int    `json:"id"`
	Code   string `json:"code"`
	NameEN string `json:"name_en"`
	NameAR string `json:"name_ar"`
	Symbol string `json:"symbol"`
}

type Language struct {
	ID     int    `json:"id"`
	Code   string `json:"code"`
	NameEN string `json:"name_en"`
	NameAR string `json:"name_ar"`
}

type Country struct {
	ID            int               `json:"id"`
	ISO2          string            `json:"iso2"`
	ISO3          string            `json:"iso3"`
	NumericCode   string            `json:"numeric_code"`
	NameEN        string            `json:"name_en"`
	NameAR        string            `json:"name_ar"`
	NativeName    string            `json:"native_name"`
	PhoneCode     string            `json:"phone_code"` // without the plus: "20"
	Emoji         string            `json:"emoji"`
	CapitalEN     string            `json:"capital_en"`
	Region        string            `json:"region"`
	TLD           string            `json:"tld"`
	NationalityEN string            `json:"nationality_en"`
	CurrencyCode  string            `json:"currency_code"`
	CurrencyID    *int              `json:"currency_id"`
	Lat           *float64          `json:"lat"`
	Lng           *float64          `json:"lng"`
	Timezones     []string          `json:"timezones"`
	Translations  map[string]string `json:"translations,omitempty"`
	Currency      *Currency         `json:"currency,omitempty"` // Country() only
}

// City is a city or governorate. A name is empty when the source has it in one language only.
type City struct {
	ID        int      `json:"id"`
	CountryID int      `json:"country_id"`
	NameEN    string   `json:"name_en"`
	NameAR    string   `json:"name_ar"`
	Lat       *float64 `json:"lat"`
	Lng       *float64 `json:"lng"`
	Timezone  string   `json:"timezone"`
	Country   *Country `json:"country,omitempty"` // City() and Area() only
}

type Area struct {
	ID     int    `json:"id"`
	CityID int    `json:"city_id"`
	NameEN string `json:"name_en"`
	NameAR string `json:"name_ar"`
	City   *City  `json:"city,omitempty"` // Area() only
}

// Hit is one search result; Type is "country", "city" or "area".
type Hit struct {
	Type          string `json:"type"`
	ID            int    `json:"id"`
	NameEN        string `json:"name_en"`
	NameAR        string `json:"name_ar"`
	CountryID     int    `json:"country_id,omitempty"`
	CityID        int    `json:"city_id,omitempty"`
	CountryISO2   string `json:"country_iso2,omitempty"`
	CountryNameEN string `json:"country_name_en,omitempty"`
	CountryNameAR string `json:"country_name_ar,omitempty"`
	CityNameEN    string `json:"city_name_en,omitempty"`
	CityNameAR    string `json:"city_name_ar,omitempty"`
}

// Address is the normalized postal address products store. Zero ids and empty
// strings mean "not given". Phone is E.164.
type Address struct {
	CountryID  int      `json:"country_id,omitempty"`
	CityID     int      `json:"city_id,omitempty"`
	AreaID     int      `json:"area_id,omitempty"`
	Street     string   `json:"street,omitempty"`
	Building   string   `json:"building,omitempty"`
	Floor      string   `json:"floor,omitempty"`
	Apartment  string   `json:"apartment,omitempty"`
	Landmark   string   `json:"landmark,omitempty"`
	PostalCode string   `json:"postal_code,omitempty"`
	Lat        *float64 `json:"lat,omitempty"`
	Lng        *float64 `json:"lng,omitempty"`
	Phone      string   `json:"phone,omitempty"`
}

// Formatted is an address written out in each language.
type Formatted struct {
	EN string `json:"en"`
	AR string `json:"ar"`
}

// Error is a non-2xx answer; Fields is set on 422 from FormatAddress.
type Error struct {
	Status  int
	Message string
	Fields  map[string]string
}

func (e *Error) Error() string { return fmt.Sprintf("circlexo locations: %d %s", e.Status, e.Message) }

// Client reads the hub's locations API.
type Client struct {
	base string
	http *http.Client
}

// New makes a client for the hub at baseURL (e.g. https://accounts.circlexo.com).
// A nil httpClient uses one with a 15 second timeout.
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{base: strings.TrimRight(baseURL, "/") + "/api/locations", http: httpClient}
}

type list[T any] struct {
	Items []T `json:"items"`
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		e := &Error{Status: res.StatusCode, Message: res.Status}
		var b struct {
			Error  string            `json:"error"`
			Fields map[string]string `json:"fields"`
		}
		if json.Unmarshal(data, &b) == nil {
			if b.Error != "" {
				e.Message = b.Error
			}
			e.Fields = b.Fields
		}
		return e
	}
	return json.Unmarshal(data, out)
}

func get[T any](ctx context.Context, c *Client, path string, q url.Values) (T, error) {
	var v T
	err := c.do(ctx, http.MethodGet, path, q, nil, &v)
	return v, err
}

func items[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	l, err := get[list[T]](ctx, c, path, q)
	return l.Items, err
}

func page(q url.Values, limit, offset int) url.Values {
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	return q
}

func (c *Client) Countries(ctx context.Context) ([]Country, error) {
	return items[Country](ctx, c, "/countries", nil)
}

// Country finds one by id, ISO 3166 alpha-2 or alpha-3 code.
func (c *Client) Country(ctx context.Context, idOrCode string) (Country, error) {
	return get[Country](ctx, c, "/countries/"+url.PathEscape(idOrCode), nil)
}

// Cities lists a country's cities; limit and offset of 0 mean the server's defaults.
func (c *Client) Cities(ctx context.Context, countryID, limit, offset int) ([]City, error) {
	return items[City](ctx, c, "/cities", page(url.Values{"country_id": {strconv.Itoa(countryID)}}, limit, offset))
}

func (c *Client) City(ctx context.Context, id int) (City, error) {
	return get[City](ctx, c, "/cities/"+strconv.Itoa(id), nil)
}

func (c *Client) Areas(ctx context.Context, cityID, limit, offset int) ([]Area, error) {
	return items[Area](ctx, c, "/areas", page(url.Values{"city_id": {strconv.Itoa(cityID)}}, limit, offset))
}

func (c *Client) Area(ctx context.Context, id int) (Area, error) {
	return get[Area](ctx, c, "/areas/"+strconv.Itoa(id), nil)
}

// SearchOptions narrows Search; zero values mean no filter.
type SearchOptions struct {
	Type      string // country, city or area
	CountryID int
	CityID    int
	Limit     int
}

// Search finds places by name prefix, English or Arabic.
func (c *Client) Search(ctx context.Context, q string, o SearchOptions) ([]Hit, error) {
	v := url.Values{"q": {q}}
	if o.Type != "" {
		v.Set("type", o.Type)
	}
	if o.CountryID > 0 {
		v.Set("country_id", strconv.Itoa(o.CountryID))
	}
	if o.CityID > 0 {
		v.Set("city_id", strconv.Itoa(o.CityID))
	}
	if o.Limit > 0 {
		v.Set("limit", strconv.Itoa(o.Limit))
	}
	return items[Hit](ctx, c, "/search", v)
}

func (c *Client) Currencies(ctx context.Context) ([]Currency, error) {
	return items[Currency](ctx, c, "/currencies", nil)
}

func (c *Client) Currency(ctx context.Context, code string) (Currency, error) {
	return get[Currency](ctx, c, "/currencies/"+url.PathEscape(code), nil)
}

func (c *Client) Languages(ctx context.Context) ([]Language, error) {
	return items[Language](ctx, c, "/languages", nil)
}

func (c *Client) Language(ctx context.Context, code string) (Language, error) {
	return get[Language](ctx, c, "/languages/"+url.PathEscape(code), nil)
}

// FormatAddress validates a (the city is in the country, the area in the city,
// a real E.164 phone) and returns it normalized and written out in English and
// Arabic. An invalid address is an *Error with Status 422 and Fields.
func (c *Client) FormatAddress(ctx context.Context, a Address) (Address, Formatted, error) {
	var out struct {
		Address   Address   `json:"address"`
		Formatted Formatted `json:"formatted"`
	}
	err := c.do(ctx, http.MethodPost, "/addresses/format", nil, a, &out)
	return out.Address, out.Formatted, err
}

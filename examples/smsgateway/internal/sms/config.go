package sms

import (
	"fmt"
	"strings"
	"time"

	"github.com/oarkflow/ref/examples/smsgateway/internal/cfgdec"
)

// HubConfig is the config block of the sms.hub resource.
type HubConfig struct {
	// Database and Queue name the resources the hub stores into and queues on.
	Database string `json:"database"`
	Queue    string `json:"queue"`
	NodeID   string `json:"node_id"`

	// Currency is the one currency the platform prices and bills in.
	Currency string `json:"currency"`
	// DefaultCountry reads national-format numbers (09841234567).
	DefaultCountry string `json:"default_country"`
	// DefaultSender is used when neither the request nor the user names one.
	DefaultSender string `json:"default_sender"`

	// MaxSegments rejects messages longer than this many segments (default 6).
	MaxSegments int `json:"max_segments"`
	// TTL is how long a message may wait before it expires unsent (default 24h).
	TTL Duration `json:"ttl"`

	// CaptureOn is when funds held at acceptance become a charge: "submit" (the
	// provider accepted the message; default) or "delivered" (the handset got
	// it).
	CaptureOn string `json:"capture_on"`
	// RefundOnDLRFailure returns a charge captured at submit when the receipt
	// later says the message failed.
	RefundOnDLRFailure bool `json:"refund_on_dlr_failure"`
	// KeepText keeps message text after a provider has accepted the message. By
	// default the text is erased then: an OTP should not outlive its delivery.
	KeepText bool `json:"keep_text"`

	Routing RoutingPolicy `json:"routing"`
	Pricing struct {
		// Default is the sell price per segment; Country overrides it per
		// destination, and Rates add prices for other combinations. The most
		// specific rate wins.
		Default float64            `json:"default"`
		Country map[string]float64 `json:"country"`
		Rates   []PriceRule        `json:"rate"`
	} `json:"pricing"`

	Pipeline struct {
		// Deliver and DLR name the intents that run for a dispatch job and for a
		// delivery receipt.
		Deliver string `json:"deliver"`
		DLR     string `json:"dlr"`
		// ClaimLease is how long a worker owns a message while it sends.
		ClaimLease Duration `json:"claim_lease"`
	} `json:"pipeline"`

	Recovery struct {
		// Interval between sweeps for messages whose job was lost; Grace is how
		// overdue a message must be before the sweep republishes it.
		Interval Duration `json:"interval"`
		Grace    Duration `json:"grace"`
		Batch    int      `json:"batch"`
	} `json:"recovery"`

	Seed struct {
		Users       []SeedUser   `json:"user"`
		Assignments []Assignment `json:"assignment"`
		Rates       []Rate       `json:"rate"`
	} `json:"seed"`
}

// PriceRule is one price in the pricing block: rate "otp_np" { country "NP"
// type "otp" price 0.03 }. Every dimension left out matches anything.
type PriceRule struct {
	ID      string  `json:"id"`
	User    string  `json:"user"`
	Country string  `json:"country"`
	Type    string  `json:"type"`
	Price   float64 `json:"price"`
}

// SeedUser is a user declared in BCL. The user is created if absent; an
// existing user is never overwritten, so changes made through the admin API
// survive a restart. Balance is credited once, ever.
type SeedUser struct {
	User
	APIKey  string  `json:"api_key"`
	Balance float64 `json:"balance"`
}

func decodeHubConfig(config map[string]any) (HubConfig, error) {
	var c HubConfig
	if err := cfgdec.Decode(config, &c); err != nil {
		return c, err
	}
	if c.Currency == "" {
		c.Currency = "USD"
	}
	c.Currency = strings.ToUpper(c.Currency)
	if c.DefaultCountry != "" {
		c.DefaultCountry = strings.ToUpper(c.DefaultCountry)
		if !KnownCountry(c.DefaultCountry) {
			return c, fmt.Errorf("default_country %q is not a known country", c.DefaultCountry)
		}
	}
	if c.DefaultSender == "" {
		c.DefaultSender = "SMS"
	}
	if c.MaxSegments <= 0 {
		c.MaxSegments = 6
	}
	if c.TTL == 0 {
		c.TTL = Duration(24 * time.Hour)
	}
	switch c.CaptureOn {
	case "":
		c.CaptureOn = "submit"
	case "submit", "delivered":
	default:
		return c, fmt.Errorf("capture_on must be submit or delivered")
	}
	if c.NodeID == "" {
		c.NodeID = "node"
	}
	if c.Pipeline.Deliver == "" {
		c.Pipeline.Deliver = "sms.deliver"
	}
	if c.Pipeline.DLR == "" {
		c.Pipeline.DLR = "sms.dlr"
	}
	if c.Pipeline.ClaimLease == 0 {
		c.Pipeline.ClaimLease = Duration(60 * time.Second)
	}
	if c.Recovery.Interval == 0 {
		c.Recovery.Interval = Duration(15 * time.Second)
	}
	if c.Recovery.Grace == 0 {
		c.Recovery.Grace = Duration(30 * time.Second)
	}
	if c.Recovery.Batch <= 0 {
		c.Recovery.Batch = 200
	}
	for _, o := range c.Routing.Objectives {
		switch o {
		case ObjectiveBalanced, ObjectiveCost, ObjectiveDelivery, ObjectiveLatency:
		default:
			return c, fmt.Errorf("routing objective %q is not balanced, lowest_cost, highest_delivery or lowest_latency", o)
		}
	}
	return c, nil
}

// baseRates turns the pricing block into rates.
func (c HubConfig) baseRates() []Rate {
	var out []Rate
	if c.Pricing.Default > 0 {
		out = append(out, Rate{ID: "base:*", SellPerSegmentMicros: FromUnits(c.Pricing.Default), Currency: c.Currency})
	}
	for country, v := range c.Pricing.Country {
		out = append(out, Rate{ID: "base:" + strings.ToUpper(country), Country: strings.ToUpper(country),
			SellPerSegmentMicros: FromUnits(v), Currency: c.Currency})
	}
	for _, r := range c.Pricing.Rates {
		out = append(out, Rate{ID: "base:" + r.ID, UserID: r.User, Country: strings.ToUpper(r.Country), MessageType: r.Type,
			SellPerSegmentMicros: FromUnits(r.Price), Currency: c.Currency})
	}
	return out
}

package invoice

import (
	"fmt"
	"time"
)

type LineItem struct {
	Description string
	Quantity    int
	UnitPrice   int64 // cents
}

type Invoice struct {
	Number   string
	Customer string
	IssuedAt time.Time
	Lines    []LineItem
}

func New(number, customer string) *Invoice {
	return &Invoice{
		Number:   number,
		Customer: customer,
		IssuedAt: time.Now(),
	}
}

func (i *Invoice) AddLine(desc string, qty int, unitCents int64) {
	i.Lines = append(i.Lines, LineItem{
		Description: desc,
		Quantity:    qty,
		UnitPrice:   unitCents,
	})
}

func (i *Invoice) Total() int64 {
	var total int64
	for _, l := range i.Lines {
		total += int64(l.Quantity) * l.UnitPrice
	}
	return total
}

func (i *Invoice) Summary() string {
	return fmt.Sprintf("invoice %s for %s: %d lines, total=%d", i.Number, i.Customer, len(i.Lines), i.Total())
}

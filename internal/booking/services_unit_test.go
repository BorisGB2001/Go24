package booking

import (
	"reflect"
	"testing"
)

// TestTotal tests sum of prices and first non-empty currency extraction.
func TestTotal(t *testing.T) {
	tests := []struct {
		name         string
		list         []Service
		wantSum      float64
		wantCurrency string
	}{
		{"nil slice", nil, 0, ""},
		{"empty slice", []Service{}, 0, ""},
		{
			"single service",
			[]Service{{ProductID: "p1", Price: 150.50, Currency: "RUB"}},
			150.50, "RUB",
		},
		{
			"multiple services same currency",
			[]Service{
				{ProductID: "p1", Price: 100.00, Currency: "RUB"},
				{ProductID: "p2", Price: 250.50, Currency: "RUB"},
			},
			350.50, "RUB",
		},
		{
			"multiple services different currencies picks first",
			[]Service{
				{ProductID: "p1", Price: 50.00, Currency: "EUR"},
				{ProductID: "p2", Price: 100.00, Currency: "USD"},
			},
			150.00, "EUR",
		},
		{
			"first service empty currency picks next non-empty",
			[]Service{
				{ProductID: "p1", Price: 10.00, Currency: ""},
				{ProductID: "p2", Price: 20.00, Currency: "USD"},
			},
			30.00, "USD",
		},
		{
			"all services empty currency",
			[]Service{
				{ProductID: "p1", Price: 10.00, Currency: ""},
				{ProductID: "p2", Price: 20.00, Currency: ""},
			},
			30.00, "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotSum, gotCur := Total(tc.list)
			if gotSum != tc.wantSum || gotCur != tc.wantCurrency {
				t.Fatalf("Total() = %v, %q; want %v, %q", gotSum, gotCur, tc.wantSum, tc.wantCurrency)
			}
			b := Booking{Services: tc.list}
			if bSum, bCur := b.Total(); bSum != gotSum || bCur != gotCur {
				t.Fatalf("Booking.Total() = %v, %q; want %v, %q", bSum, bCur, gotSum, gotCur)
			}
		})
	}
}

// TestMinutes tests total duration summation across services.
func TestMinutes(t *testing.T) {
	tests := []struct {
		name string
		list []Service
		want int
	}{
		{"nil", nil, 0},
		{"empty", []Service{}, 0},
		{"single", []Service{{Minutes: 45}}, 45},
		{"multiple", []Service{{Minutes: 30}, {Minutes: 60}, {Minutes: 15}}, 105},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Minutes(tc.list); got != tc.want {
				t.Fatalf("Minutes() = %d; want %d", got, tc.want)
			}
		})
	}
}

// TestBookingServiceIDs tests extracting non-empty product IDs from a booking.
func TestBookingServiceIDs(t *testing.T) {
	tests := []struct {
		name string
		b    Booking
		want []string
	}{
		{"nil services", Booking{Services: nil}, []string{}},
		{"empty services", Booking{Services: []Service{}}, []string{}},
		{
			"valid and empty product IDs",
			Booking{Services: []Service{
				{ProductID: "p1", Name: "Consultation"},
				{ProductID: "", Name: "Custom fee"},
				{ProductID: "p2", Name: "Procedure"},
			}},
			[]string{"p1", "p2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.b.ServiceIDs()
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Booking.ServiceIDs() = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestKeep tests preserving existing booked service details while updating product selections.
func TestKeep(t *testing.T) {
	old := []Service{
		{ProductID: "p1", Name: "Old P1", Price: 100, Currency: "USD", Minutes: 30},
		{ProductID: "p2", Name: "Old P2", Price: 200, Currency: "USD", Minutes: 60},
	}

	tests := []struct {
		name string
		ids  []string
		want []Service
	}{
		{"empty ids", []string{}, []Service{}},
		{
			"retain old services in given order",
			[]string{"p2", "p1"},
			[]Service{old[1], old[0]},
		},
		{
			"new product gets blank service with product ID",
			[]string{"p3"},
			[]Service{{ProductID: "p3"}},
		},
		{
			"mix of existing and new products",
			[]string{"p1", "p3", "p2"},
			[]Service{old[0], {ProductID: "p3"}, old[1]},
		},
		{
			"repeated product IDs",
			[]string{"p1", "p1"},
			[]Service{old[0], old[0]},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Keep(tc.ids, old)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Keep() = %+v; want %+v", got, tc.want)
			}
		})
	}
}

// TestResourceServiceLookup tests finding a specific product among resource services.
func TestResourceServiceLookup(t *testing.T) {
	svc1 := Service{ProductID: "p1", Minutes: 30}
	svc2 := Service{ProductID: "p2", Minutes: 60}
	r := Resource{Services: []Service{svc1, svc2}}

	got, ok := r.Service("p1")
	if !ok || got != svc1 {
		t.Fatalf("r.Service(p1) = %v, %v; want %v, true", got, ok, svc1)
	}

	got, ok = r.Service("p2")
	if !ok || got != svc2 {
		t.Fatalf("r.Service(p2) = %v, %v; want %v, true", got, ok, svc2)
	}

	got, ok = r.Service("p3")
	if ok || got != (Service{}) {
		t.Fatalf("r.Service(p3) = %v, %v; want empty, false", got, ok)
	}
}

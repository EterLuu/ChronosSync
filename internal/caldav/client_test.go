package caldav

import (
	"testing"
	"time"

	"github.com/emersion/go-ical"
	webcaldav "github.com/emersion/go-webdav/caldav"
)

func TestConvertCalDAVObjectExpandsWeeklyAndMonthlyEvents(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	windowStart := time.Date(2026, 7, 21, 10, 0, 0, 0, loc)
	windowEnd := time.Date(2026, 7, 23, 0, 0, 0, 0, loc)

	tests := []struct {
		name      string
		uid       string
		start     time.Time
		rrule     string
		wantStart time.Time
	}{
		{
			name:      "weekly",
			uid:       "weekly@example.test",
			start:     time.Date(2026, 7, 1, 15, 0, 0, 0, loc),
			rrule:     "FREQ=WEEKLY;BYDAY=WE",
			wantStart: time.Date(2026, 7, 22, 15, 0, 0, 0, loc),
		},
		{
			name:      "monthly",
			uid:       "monthly@example.test",
			start:     time.Date(2026, 1, 22, 9, 30, 0, 0, loc),
			rrule:     "FREQ=MONTHLY;BYMONTHDAY=22",
			wantStart: time.Date(2026, 7, 22, 9, 30, 0, 0, loc),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := ical.NewComponent(ical.CompEvent)
			component.Props.SetText(ical.PropUID, tt.uid)
			component.Props.SetText(ical.PropSummary, tt.name)
			component.Props.SetDateTime(ical.PropDateTimeStart, tt.start)
			component.Props.SetDateTime(ical.PropDateTimeEnd, tt.start.Add(time.Hour))
			rule := ical.NewProp(ical.PropRecurrenceRule)
			rule.Value = tt.rrule
			component.Props.Set(rule)

			calendar := ical.NewCalendar()
			calendar.Children = append(calendar.Children, component)
			object := webcaldav.CalendarObject{
				Path: "/calendar/" + tt.uid + ".ics",
				ETag: "etag-1",
				Data: calendar,
			}

			items, err := convertCalDAVObjectToEventItems(object, windowStart, windowEnd)
			if err != nil {
				t.Fatalf("convert event: %v", err)
			}
			if len(items) != 1 {
				t.Fatalf("got %d instances, want 1: %#v", len(items), items)
			}
			if items[0].StartTime == nil || !items[0].StartTime.Equal(tt.wantStart) {
				t.Fatalf("start = %v, want %v", items[0].StartTime, tt.wantStart)
			}
			if !items[0].Recurring || items[0].RecurrenceID == nil {
				t.Fatalf("instance was not marked recurring: %#v", items[0])
			}
		})
	}
}

func TestConvertCalDAVObjectUsesRecurrenceOverride(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	originalSlot := time.Date(2026, 7, 22, 9, 0, 0, 0, loc)
	movedStart := time.Date(2026, 7, 22, 14, 0, 0, 0, loc)

	master := ical.NewComponent(ical.CompEvent)
	master.Props.SetText(ical.PropUID, "override@example.test")
	master.Props.SetText(ical.PropSummary, "original")
	master.Props.SetDateTime(ical.PropDateTimeStart, time.Date(2026, 7, 1, 9, 0, 0, 0, loc))
	master.Props.SetDateTime(ical.PropDateTimeEnd, time.Date(2026, 7, 1, 10, 0, 0, 0, loc))
	rule := ical.NewProp(ical.PropRecurrenceRule)
	rule.Value = "FREQ=WEEKLY;BYDAY=WE"
	master.Props.Set(rule)

	override := ical.NewComponent(ical.CompEvent)
	override.Props.SetText(ical.PropUID, "override@example.test")
	override.Props.SetText(ical.PropSummary, "moved")
	override.Props.SetDateTime(ical.PropRecurrenceID, originalSlot)
	override.Props.SetDateTime(ical.PropDateTimeStart, movedStart)
	override.Props.SetDateTime(ical.PropDateTimeEnd, movedStart.Add(time.Hour))

	calendar := ical.NewCalendar()
	calendar.Children = append(calendar.Children, master, override)
	items, err := convertCalDAVObjectToEventItems(webcaldav.CalendarObject{Data: calendar},
		time.Date(2026, 7, 21, 0, 0, 0, 0, loc),
		time.Date(2026, 7, 23, 0, 0, 0, 0, loc))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Summary != "moved" || !items[0].StartTime.Equal(movedStart) {
		t.Fatalf("override expansion = %#v", items)
	}
	if items[0].RecurrenceID == nil || !items[0].RecurrenceID.Equal(originalSlot) {
		t.Fatalf("recurrence id = %v, want %v", items[0].RecurrenceID, originalSlot)
	}
}

func TestConvertCalDAVObjectAppliesRDATEAndEXDATELists(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	windowStart := time.Date(2026, 7, 21, 0, 0, 0, 0, loc)
	windowEnd := time.Date(2026, 7, 23, 0, 0, 0, 0, loc)

	rdateEvent := ical.NewComponent(ical.CompEvent)
	rdateEvent.Props.SetText(ical.PropUID, "rdate")
	rdateEvent.Props.SetDateTime(ical.PropDateTimeStart, time.Date(2026, 7, 1, 9, 0, 0, 0, loc))
	rdateEvent.Props.SetDateTime(ical.PropDateTimeEnd, time.Date(2026, 7, 1, 10, 0, 0, 0, loc))
	rdates := ical.NewProp(ical.PropRecurrenceDates)
	rdates.Params.Set(ical.PropTimezoneID, "Asia/Shanghai")
	rdates.Value = "20260715T090000,20260722T090000"
	rdateEvent.Props.Set(rdates)

	excludedEvent := ical.NewComponent(ical.CompEvent)
	excludedEvent.Props.SetText(ical.PropUID, "excluded")
	excludedEvent.Props.SetDateTime(ical.PropDateTimeStart, time.Date(2026, 7, 1, 11, 0, 0, 0, loc))
	excludedEvent.Props.SetDateTime(ical.PropDateTimeEnd, time.Date(2026, 7, 1, 12, 0, 0, 0, loc))
	rule := ical.NewProp(ical.PropRecurrenceRule)
	rule.Value = "FREQ=WEEKLY;BYDAY=WE"
	excludedEvent.Props.Set(rule)
	exdates := ical.NewProp(ical.PropExceptionDates)
	exdates.Params.Set(ical.PropTimezoneID, "Asia/Shanghai")
	exdates.Value = "20260715T110000,20260722T110000"
	excludedEvent.Props.Set(exdates)

	calendar := ical.NewCalendar()
	calendar.Children = append(calendar.Children, rdateEvent, excludedEvent)
	items, err := convertCalDAVObjectToEventItems(webcaldav.CalendarObject{Data: calendar}, windowStart, windowEnd)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].UID != "rdate" {
		t.Fatalf("RDATE/EXDATE expansion = %#v", items)
	}
	want := time.Date(2026, 7, 22, 9, 0, 0, 0, loc)
	if items[0].StartTime == nil || !items[0].StartTime.Equal(want) {
		t.Fatalf("RDATE start=%v, want %v", items[0].StartTime, want)
	}
}

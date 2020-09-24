package resources

import (
	"fmt"
	"sort"
	"strings"

	billing "github.com/googleinterns/terraform-cost-estimation/billing"
	"github.com/googleinterns/terraform-cost-estimation/io/web"
	conv "github.com/googleinterns/terraform-cost-estimation/memconverter"
	dsk "github.com/googleinterns/terraform-cost-estimation/resources/classdetail/disk"
	img "github.com/googleinterns/terraform-cost-estimation/resources/classdetail/image"
)

// ComputeDisk holds information about the compute disk resource type.
type ComputeDisk struct {
	Name        string
	ID          string
	Type        string
	Regional    bool
	Zones       []string
	Region      string
	Image       string
	Snapshot    string
	SizeGiB     int64
	Description Description
	UnitPricing PricingInfo
}

// NewComputeDisk builds a compute disk with the specified fields and fills the other resource details.
// Image, snapshot and size parameters are considered null fields when "" or <= 0.
// Returns a pointer to a ComputeInstance structure, or nil and error upon failure.
// Currently not supported: snapshots.
func NewComputeDisk(name, id, diskType string, zones []string, image, snapshot string, size int64) (*ComputeDisk, error) {
	disk := &ComputeDisk{Name: name, ID: id, Type: diskType, Zones: zones}
	disk.Description.fillForComputeDisk(diskType, len(zones) > 1)

	i := strings.LastIndex(zones[0], "-")
	if i < 0 {
		return nil, fmt.Errorf("invalid zone format")
	}
	disk.Region = zones[0][:i]

	def, min, max, err := dsk.Details(diskType, zones[0], disk.Region)
	if err != nil {
		return nil, err
	}

	switch {
	case image == "":
		if size <= 0 {
			disk.SizeGiB = def
		} else {
			disk.SizeGiB = size
		}

	case size <= 0:
		s, err := img.GetImageDiskSize(image)
		if err != nil {
			return nil, err
		}
		disk.SizeGiB = s

	default:
		s, err := img.GetImageDiskSize(image)
		if err != nil {
			return nil, err
		}
		if size < s {
			return nil, fmt.Errorf("size should at least be the size of the specified image")
		}
		disk.SizeGiB = size
	}

	if disk.SizeGiB < min || disk.SizeGiB > max {
		return nil, fmt.Errorf("size is not in the valid range")
	}

	return disk, nil
}

func (disk *ComputeDisk) completePricingInfo(catalog *billing.ComputeEngineCatalog) error {
	skus, err := catalog.DiskSKUs(disk.Type)
	if err != nil {
		return err
	}

	filtered, err := filterSKUs(skus, disk.Region, disk.Description)
	if err != nil {
		return err
	}

	if len(filtered) == 0 {
		return fmt.Errorf("could not find disk pricing information")
	}

	disk.UnitPricing.fillMonthlyBase(filtered[0])

	return nil
}

func (disk *ComputeDisk) totalPrice() (float64, error) {
	monthlyToHourly := 1.0 / float64(30*24)

	units, err := conv.Convert("gib", float64(disk.SizeGiB), strings.Split(disk.UnitPricing.UsageUnit, " ")[0])
	if err != nil {
		return 0, err
	}

	return units * float64(disk.UnitPricing.HourlyUnitPrice) / nano * monthlyToHourly, nil
}

// ComputeDiskState holdsthe before and after states of a compute disk and the action performed.
type ComputeDiskState struct {
	Before *ComputeDisk
	After  *ComputeDisk
	Action string
}

// CompletePricingInfo completes pricing information of both before and after states.
func (state *ComputeDiskState) CompletePricingInfo(catalog *billing.ComputeEngineCatalog) error {
	if state.Before != nil {
		if err := state.Before.completePricingInfo(catalog); err != nil {
			return fmt.Errorf(state.Before.Name + "(" + state.Before.Type + ")" + ": " + err.Error())
		}
	}

	if state.After != nil {
		if err := state.After.completePricingInfo(catalog); err != nil {
			return fmt.Errorf(state.After.Name + "(" + state.After.Type + ")" + ": " + err.Error())
		}
	}

	return nil
}

func (state *ComputeDiskState) generalChanges() (name, id, action, diskType, zones, image, snapshot string) {
	action = state.Action
	// Before and After can't be nil at the same time. Take return values from the non nil state or a combination of both.
	switch {
	case state.Before == nil:
		name = state.After.Name
		if state.After.ID == "" {
			id = "unknown"
		} else {
			id = state.After.ID
		}
		diskType = state.After.Type
		image = state.After.Image
		snapshot = state.After.Snapshot

		sort.Strings(state.After.Zones)
		zones = strings.Join(state.After.Zones, ", ")

	case state.After == nil:
		name = state.Before.Name
		id = state.Before.ID
		diskType = state.Before.Type
		image = state.Before.Image
		snapshot = state.Before.Snapshot

		sort.Strings(state.Before.Zones)
		zones = strings.Join(state.Before.Zones, ", ")

	default:
		name = generalChange(state.Before.Name, state.After.Name)
		id = state.Before.ID
		diskType = generalChange(state.Before.Type, state.After.Type)
		image = generalChange(state.Before.Image, state.After.Image)
		snapshot = generalChange(state.Before.Snapshot, state.After.Snapshot)
		zones = zonesChange(state.Before.Zones, state.After.Zones)
	}
	return
}

func (state *ComputeDiskState) costChanges() (costPerUnit1, costPerUnit2 float64, units1, units2 int64, delta float64, err error) {
	if state.Before != nil {
		costPerUnit1 = float64(state.Before.UnitPricing.HourlyUnitPrice)
		u1, err := conv.Convert("gib", float64(state.Before.SizeGiB), strings.Split(state.Before.UnitPricing.UsageUnit, " ")[0])
		if err != nil {
			return 0, 0, 0, 0, 0, err
		}
		units1 = int64(u1)
	}

	if state.After != nil {
		costPerUnit2 = float64(state.After.UnitPricing.HourlyUnitPrice)
		u2, err := conv.Convert("gib", float64(state.After.SizeGiB), strings.Split(state.After.UnitPricing.UsageUnit, " ")[0])
		if err != nil {
			return 0, 0, 0, 0, 0, err
		}
		units2 = int64(u2)
	}

	delta = costPerUnit2*float64(units2) - costPerUnit1*float64(units1)

	return
}

// GetWebTables returns html pricing information table strings to be displayed in a web page.
func (state *ComputeDiskState) GetWebTables(stateNum int) (*web.PricingTypeTables, error) {
	name, id, action, diskType, zones, image, snapshot := state.generalChanges()
	costPerUnit1, costPerUnit2, units1, units2, delta, err := state.costChanges()
	if err != nil {
		return nil, err
	}

	h := web.Table{Index: stateNum, Type: "hourly"}
	h.AddComputeDiskGeneralInfo(name, id, action, diskType, zones, image, snapshot)
	h.AddComputeDiskPricing("hour", costPerUnit1, costPerUnit2, units1, units2, delta)

	m := web.Table{Index: stateNum, Type: "monthly"}
	m.AddComputeDiskGeneralInfo(name, id, action, diskType, zones, image, snapshot)
	m.AddComputeDiskPricing("month", costPerUnit1/hourlyToMonthly, costPerUnit2/hourlyToMonthly, units1, units2, delta/hourlyToMonthly)

	y := web.Table{Index: stateNum, Type: "yearly"}
	y.AddComputeDiskGeneralInfo(name, id, action, diskType, zones, image, snapshot)
	y.AddComputeDiskPricing("year", costPerUnit1/hourlyToYearly, costPerUnit2/hourlyToYearly, units1, units2, delta/hourlyToYearly)

	return &web.PricingTypeTables{Hourly: h, Monthly: m, Yearly: y}, nil
}
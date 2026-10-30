package inputsource

import (
	"errors"
	"fmt"
)

var (
	errEmptyInputSourceID    = errors.New("inputsource: empty source ID")
	errInputSourceNotEnabled = errors.New("inputsource: source is not enabled")
)

type enabledSourceListAPI struct {
	createList   func(uintptr, uint8) uintptr
	arrayCount   func(uintptr) int64
	arrayValue   func(uintptr, int64) uintptr
	sourceID     func(uintptr) (string, error)
	selectSource func(uintptr) error
	release      func(uintptr)
}

func selectEnabledFromList(id string, api enabledSourceListAPI) error {
	if id == "" {
		return errEmptyInputSourceID
	}
	if api.createList == nil || api.arrayCount == nil || api.arrayValue == nil || api.sourceID == nil || api.selectSource == nil || api.release == nil {
		return errors.New("inputsource: incomplete enabled-source API")
	}
	list := api.createList(0, 0) // includeAllSources=false: enabled sources only.
	if list == 0 {
		return errors.New("inputsource: could not enumerate enabled sources")
	}
	defer api.release(list)

	count := api.arrayCount(list)
	if count < 0 {
		return errors.New("inputsource: invalid enabled-source list")
	}
	return selectExactEnabledSource(id, int(count), func(index int) (uintptr, string, error) {
		source := api.arrayValue(list, int64(index))
		if source == 0 {
			return 0, "", nil
		}
		sourceID, err := api.sourceID(source)
		return source, sourceID, err
	}, api.selectSource)
}

func selectExactEnabledSource(
	id string,
	count int,
	sourceAt func(int) (uintptr, string, error),
	selectSource func(uintptr) error,
) error {
	if id == "" {
		return errEmptyInputSourceID
	}
	for index := 0; index < count; index++ {
		source, sourceID, err := sourceAt(index)
		if err != nil {
			return err
		}
		if source == 0 || sourceID != id {
			continue
		}
		return selectSource(source)
	}
	return fmt.Errorf("%w: %q", errInputSourceNotEnabled, id)
}

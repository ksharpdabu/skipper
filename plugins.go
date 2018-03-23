package skipper

import (
	"fmt"
	"os"
	"path/filepath"
	"plugin"
	"strings"

	"github.com/zalando/skipper/filters"
	"github.com/zalando/skipper/routing"
)

func findAndLoadPlugins(o *Options) {
	found := make(map[string]string)

	for _, dir := range o.PluginDirs {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ".so") {
				name := filepath.Base(path)
				name = name[:len(name)-3] // strip suffix
				found[name] = path
				fmt.Printf("found plugin %s at %s\n", name, path)
			}
			return nil
		})
	}

	for _, plug := range o.MultiPlugins {
		name := plug[0]
		path, ok := found[name]
		if !ok {
			fmt.Printf("mutlitype plugin %s not found in plugin dirs\n", name)
			continue
		}
		fltr, pred, dc, err := LoadMultiPlugin(path, plug[1:])
		if err != nil {
			fmt.Printf("failed to load plugin %s: %s\n", path, err)
			continue
		}
		if fltr != nil {
			o.CustomFilters = append(o.CustomFilters, fltr)
		}
		if pred != nil {
			o.CustomPredicates = append(o.CustomPredicates, pred)
		}
		if dc != nil {
			o.CustomDataClients = append(o.CustomDataClients, dc)
		}
		fmt.Printf("loaded plugin %s from %s\n", name, path)
		delete(found, name)
	}

	for _, fltr := range o.FilterPlugins {
		name := fltr[0]
		path, ok := found[name]
		if !ok {
			fmt.Printf("filter plugin %s not found in plugin dirs\n", name)
			continue
		}
		spec, err := LoadFilterPlugin(path, fltr[1:])
		if err != nil {
			fmt.Printf("failed to load plugin %s: %s\n", path, err)
			continue
		}
		o.CustomFilters = append(o.CustomFilters, spec)
		fmt.Printf("loaded plugin %s (%s) from %s\n", name, spec.Name(), path)
		delete(found, name)
	}

	for _, pred := range o.PredicatePlugins {
		name := pred[0]
		path, ok := found[name]
		if !ok {
			fmt.Printf("predicate plugin %s not found in plugin dirs\n", name)
			continue
		}
		spec, err := LoadPredicatePlugin(path, pred[1:])
		if err != nil {
			fmt.Printf("failed to load plugin %s: %s\n", path, err)
			continue
		}
		o.CustomPredicates = append(o.CustomPredicates, spec)
		fmt.Printf("loaded plugin %s (%s) from %s\n", name, spec.Name(), path)
		delete(found, name)
	}

	for _, pred := range o.DataClientPlugins {
		name := pred[0]
		path, ok := found[name]
		if !ok {
			fmt.Printf("data client plugin %s not found in plugin dirs\n", name)
			continue
		}
		spec, err := LoadDataClientPlugin(path, pred[1:])
		if err != nil {
			fmt.Printf("failed to load plugin %s: %s\n", path, err)
			continue
		}
		o.CustomDataClients = append(o.CustomDataClients, spec)
		fmt.Printf("loaded plugin %s from %s\n", name, path)
		delete(found, name)
	}

	for name, path := range found {
		fmt.Printf("attempting to load plugin from %s\n", path)
		mod, err := plugin.Open(path)
		if err != nil {
			fmt.Printf("open plugin %s from %s: %s\n", name, path, err)
			continue
		}

		if sym, err := mod.Lookup("InitFilter"); err == nil {
			spec, err := loadFilterPlugin(sym, path, []string{})
			if err != nil {
				fmt.Printf("filter plugin %s returned: %s\n", path, err)
				continue
			}
			o.CustomFilters = append(o.CustomFilters, spec)
			fmt.Printf("filter plugin %s loaded from %s\n", name, path)
		}

		if sym, err := mod.Lookup("InitPredicate"); err == nil {
			spec, err := loadPredicatePlugin(sym, path, []string{})
			if err != nil {
				fmt.Printf("predicate plugin %s returned: %s\n", path, err)
				continue
			}
			o.CustomPredicates = append(o.CustomPredicates, spec)
			fmt.Printf("predicate plugin %s loaded from %s\n", name, path)
		}

		fmt.Printf("checking %s for data client in %s\n", name, path)
		if sym, err := mod.Lookup("InitDataClient"); err == nil {
			spec, err := loadDataClientPlugin(sym, path, []string{})
			if err != nil {
				fmt.Printf("data client plugin %s returned: %s\n", path, err)
				continue
			}
			o.CustomDataClients = append(o.CustomDataClients, spec)
			fmt.Printf("data client plugin %s loaded from %s\n", name, path)
		}
	}
}

func LoadMultiPlugin(path string, args []string) (filters.Spec, routing.PredicateSpec, routing.DataClient, error) {
	mod, err := plugin.Open(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open multitype plugin %s: %s", path, err)
	}
	sym, err := mod.Lookup("InitPlugin")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("lookup module symbol failed for %s: %s", path, err)
	}
	fn, ok := sym.(func([]string) (filters.Spec, routing.PredicateSpec, routing.DataClient, error))
	if !ok {
		return nil, nil, nil, fmt.Errorf("plugin %s's InitPlugin function has wrong signature", path)
	}
	fltr, pred, dc, err := fn(args)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("plugin %s returned: %s", path, err)
	}
	return fltr, pred, dc, nil
}

func LoadFilterPlugin(path string, args []string) (filters.Spec, error) {
	mod, err := plugin.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open filter plugin %s: %s", path, err)
	}
	sym, err := mod.Lookup("InitFilter")
	if err != nil {
		return nil, fmt.Errorf("lookup module symbol failed for %s: %s", path, err)
	}
	return loadFilterPlugin(sym, path, args)
}

func loadFilterPlugin(sym plugin.Symbol, path string, args []string) (filters.Spec, error) {
	fn, ok := sym.(func([]string) (filters.Spec, error))
	if !ok {
		return nil, fmt.Errorf("plugin %s's InitFilter function has wrong signature", path)
	}
	spec, err := fn(args)
	if err != nil {
		return nil, fmt.Errorf("plugin %s returned: %s", path, err)
	}
	return spec, nil
}

func LoadPredicatePlugin(path string, args []string) (routing.PredicateSpec, error) {
	mod, err := plugin.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open predicate module %s: %s", path, err)
	}
	sym, err := mod.Lookup("InitPredicate")
	if err != nil {
		return nil, fmt.Errorf("lookup module symbol failed for %s: %s", path, err)
	}
	return loadPredicatePlugin(sym, path, args)
}

func loadPredicatePlugin(sym plugin.Symbol, path string, args []string) (routing.PredicateSpec, error) {
	fn, ok := sym.(func([]string) (routing.PredicateSpec, error))
	if !ok {
		return nil, fmt.Errorf("plugin %s's InitPredicate function has wrong signature", path)
	}
	spec, err := fn(args)
	if err != nil {
		return nil, fmt.Errorf("plugin %s returned: %s", path, err)
	}
	return spec, nil
}

func LoadDataClientPlugin(path string, args []string) (routing.DataClient, error) {
	mod, err := plugin.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open data client module %s: %s", path, err)
	}
	sym, err := mod.Lookup("InitDataClient")
	if err != nil {
		return nil, fmt.Errorf("lookup module symbol failed for %s: %s", path, err)
	}
	return loadDataClientPlugin(sym, path, args)
}

func loadDataClientPlugin(sym plugin.Symbol, path string, args []string) (routing.DataClient, error) {
	fn, ok := sym.(func([]string) (routing.DataClient, error))
	if !ok {
		return nil, fmt.Errorf("plugin %s's InitDataClient function has wrong signature", path)
	}
	spec, err := fn(args)
	if err != nil {
		return nil, fmt.Errorf("module %s returned: %s", path, err)
	}
	return spec, nil
}
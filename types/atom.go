package types

// Atom 聚合 Data 和 Ability（均使用 Any 接口以容纳不同泛型实现）。
type Atom struct {
	Name       string                // 原子名称
	DataMap    map[string]AnyData    // Data keyed by name
	AbilityMap map[string]AnyAbility // Ability keyed by name
}

// 获得Atom名称
func (a *Atom) GetName() string {
	return a.Name
}

// 获得Atom的Data
func (a *Atom) GetAllData() map[string]AnyData {
	return a.DataMap
}

// 获得Atom的Ability
func (a *Atom) GetAllAbility() map[string]AnyAbility {
	return a.AbilityMap
}

// 修改Atom名称
func (a *Atom) SetName(name string) {
	a.Name = name
}

// 新增Ability
func (a *Atom) AddAbility(ability AnyAbility) {
	if a.AbilityMap == nil {
		a.AbilityMap = make(map[string]AnyAbility)
	}
	a.AbilityMap[ability.GetName()] = ability
}

// 新增Data
func (a *Atom) AddData(data AnyData) {
	if a.DataMap == nil {
		a.DataMap = make(map[string]AnyData)
	}
	a.DataMap[data.GetName()] = data
}

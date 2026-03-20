package types

// Atom 聚合 Data 和 Ability
type Atom struct {
	Name       string             // 原子名称
	DataMap    map[string]Data    // Data keyed by name
	AbilityMap map[string]Ability // Ability keyed by name
}

// 获得Atom名称
func (a *Atom) GetName() string {
	return a.Name
}

// 获得Atom的Data
func (a *Atom) GetAllData() map[string]Data {
	return a.DataMap
}

// 获得Atom的Ability
func (a *Atom) GetAllAbility() map[string]Ability {
	return a.AbilityMap
}

// 修改Atom名称
func (a *Atom) SetName(name string) {
	a.Name = name
}

// 新增Ability
func (a *Atom) AddAbility(ability Ability) {
	if a.AbilityMap == nil {
		a.AbilityMap = make(map[string]Ability)
	}
	a.AbilityMap[ability.GetName()] = ability
}

// 新增Data
func (a *Atom) AddData(data Data) {
	if a.DataMap == nil {
		a.DataMap = make(map[string]Data)
	}
	a.DataMap[data.GetName()] = data
}

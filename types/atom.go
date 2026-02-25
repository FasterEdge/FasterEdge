package types

type Atom struct {
	Name         string    // 原子名称
	DataSlice    []Data    // Data
	AbilitySlice []Ability // Ability
}

// 获得Atom名称
func (a *Atom) GetName() string {
	return a.Name
}

// 获得Atom的Data
func (a *Atom) GetAllData() []Data {
	return a.DataSlice
}

// 获得Atom的Ability
func (a *Atom) GetAllAbility() []Ability {
	return a.AbilitySlice
}

// 修改Atom名称
func (a *Atom) SetName(name string) {
	a.Name = name
}

// 新增Ability
func (a *Atom) AddAbility(ability Ability) {
	a.AbilitySlice = append(a.AbilitySlice, ability)
}

// 新增Data
func (a *Atom) AddData(data Data) {
	a.DataSlice = append(a.DataSlice, data)
}

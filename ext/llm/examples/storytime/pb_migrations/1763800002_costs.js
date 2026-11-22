/// <reference path="../types.d.ts" />
// costs: one row per model call, with what it cost in millionths of a dollar (usage.costMicroUSD). The day's total is checked against the daily
// budget before a story or a suggestion starts. Only the app reads and writes it.
migrate((app) => {
  const costs = new Collection({
    type: 'base',
    name: 'costs',
    listRule: null,
    viewRule: null,
    createRule: null,
    updateRule: null,
    deleteRule: null,
    fields: [
      { name: 'kind', type: 'text', max: 40 }, // write, speech, align, place, image, music, inspire
      { name: 'model', type: 'text', max: 200 },
      { name: 'micro_usd', type: 'number', onlyInt: true },
      { name: 'story', type: 'text', max: 40 },
      { name: 'created', type: 'autodate', onCreate: true },
    ],
    indexes: ['CREATE INDEX idx_costs_created ON costs (created)'],
  })
  app.save(costs)
}, (app) => {
  app.delete(app.findCollectionByNameOrId('costs'))
})

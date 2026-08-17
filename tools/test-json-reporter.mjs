export default async function* jsonReporter(source) {
  for await (const event of source) {
    yield `${JSON.stringify(event)}\n`;
  }
}

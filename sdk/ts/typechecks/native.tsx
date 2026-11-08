import { Button, Text, View, Section, Row, Field, Grid, Fragment, callback, useState, useLoadEffect, useFetch, type Node } from "@persona/extension/ui";

const initializeCapability: import("@persona/extension/wire").InitializeResult = { protocol: 1, id: "example", version: "1", ui_page_cancellation: 1 };
void initializeCapability;

function Counter({ label }: { label: string }): Node {
  const [count, setCount] = useState(0);
  useLoadEffect(async signal => { if (!signal.aborted) setCount(value => value); }, []);
  useFetch("https://example.com/data", { snapshot: "wait" });
  return <Section key="counter-section"><Text key="count" text={String(count)} /><Button key="increment" title={label} action={callback(() => setCount((value) => value + 1))} /></Section>;
}
export const fragments = <View key="fragments"><><Fragment key="fragment"><Section key="section" /></Fragment></></View>;
export const valid = <View key="page" title="fixture"><Counter key="counter" label="fixture" /><Section key="controls"><Row key="row" title="fixture" control={{ kind: "switch", name: "enabled", action: callback(() => {}) }} /><Field key="field" name="name" label="fixture" kind="text" /><Grid key="tiles" tiles={[{ key: "tile", title: "fixture", action: callback(() => {}) }]} /></Section></View>;

// @ts-expect-error Unknown native prop.
const unknownProp = <Text key="text" text="fixture" css="arbitrary" />;
// @ts-expect-error Native props have schema-derived types.
const wrongType = <Text key="text" text={42} />;
// @ts-expect-error Native required prop.
const missingProp = <Button key="button" title="fixture" />;
// @ts-expect-error Native keys are required.
const missingKey = <Text text="fixture" />;
// @ts-expect-error Arbitrary style object is not a native text style.
const arbitraryStyle = <Text key="text" text="fixture" style={{ color: "red" }} />;
// @ts-expect-error DOM elements do not exist in this JSX vocabulary.
const dom = <div />;
// @ts-expect-error Unknown Pascal components are not declared.
const unknownElement = <UnknownWidget key="widget" />;
// @ts-expect-error Callback code must be registered, never raw client props.
const clientCode = <Button key="button" title="fixture" action={() => {}} />;
// @ts-expect-error Unknown nested native action prop.
const invalidAction = <Button key="button" title="fixture" action={{ id: "legacy", onClick: "code" }} />;
// @ts-expect-error Assistant-only action fields are excluded.
const assistantAction = <Button key="button" title="fixture" action={{ id: "legacy", reply: "fixture" }} />;
// @ts-expect-error Assistant-only pages are excluded.
const assistantPage = <View key="page" pages={{}} />;
// Native snapshot metadata is preserved; server polling should use useInterval.
const polling = <View key="page" refresh_after={30} />;
// @ts-expect-error typed callback values require an explicit runtime parser
const wrongParser = callback<number>(value => value, {});
// @ts-expect-error unknown lowercase extension components are rejected
const unknownNative = <custom-native key="custom" />;
// @ts-expect-error unknown function component properties are rejected
const wrongChild = <Counter key="counter" label={42} />;
// @ts-expect-error Grid tiles have explicit stable keys.
const missingTileKey = <Grid key="grid" tiles={[{ title: "fixture" }]} />;
// @ts-expect-error Unknown nested native control prop.
const wrongControl = <Row key="row" title="fixture" control={{ kind: "switch", name: "enabled", action: { id: "legacy" }, script: "fixture" }} />;
// @ts-expect-error Leaf components never accept JSX children.
const leafChildren = <Text key="text" text="fixture"><Text key="child" text="fixture" /></Text>;
// @ts-expect-error HTML is not an exported extension native component.
import { Html } from "@persona/extension/ui";

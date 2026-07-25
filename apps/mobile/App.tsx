import React from "react";
import { StatusBar } from "expo-status-bar";
import { NavigationContainer, DefaultTheme } from "@react-navigation/native";
import { createBottomTabNavigator } from "@react-navigation/bottom-tabs";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { SafeAreaProvider } from "react-native-safe-area-context";
import { Ionicons } from "@expo/vector-icons";
import { api } from "./src/api/client";
import { config } from "./src/config";
import { colors } from "./src/theme";
import { Notice, Screen } from "./src/components/ui";
import ArrivalsScreen from "./src/screens/ArrivalsScreen";
import DrtScreen from "./src/screens/DrtScreen";
import AlertsScreen from "./src/screens/AlertsScreen";
import CarbonScreen from "./src/screens/CarbonScreen";
import DriverScreen from "./src/screens/DriverScreen";
import OnboardingScreen from "./src/screens/OnboardingScreen";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, staleTime: 10_000 } },
});

const Tab = createBottomTabNavigator();

const navTheme = {
  ...DefaultTheme,
  colors: {
    ...DefaultTheme.colors,
    background: colors.bg,
    card: colors.card,
    primary: colors.accent,
    text: colors.text,
    border: colors.border,
  },
};

type IoniconName = React.ComponentProps<typeof Ionicons>["name"];

const TAB_ICONS: Record<string, IoniconName> = {
  Arrivals: "bus-outline",
  DRT: "navigate-outline",
  Alerts: "alert-circle-outline",
  Carbon: "leaf-outline",
  Driver: "person-outline",
};

/**
 * Tab visibility follows the feature toggles (SPEC §3.2): a module that is OFF
 * disappears from navigation. The toggle map polls every 30s, fail-closed.
 */
function Tabs() {
  const toggles = useQuery({
    queryKey: ["toggles"],
    queryFn: api.getToggles,
    refetchInterval: config.togglesPollMs,
  });
  const t = toggles.data ?? {};
  // Until the first fetch succeeds, show the citizen surfaces (read-only);
  // the fail-closed rule applies once we know the service is unreachable.
  const loaded = toggles.isSuccess;
  const on = (module: string) => (loaded ? t[module] === true : true);

  const citizenVisible = on("passenger-pwa") || on("demand-responsive") || on("carbon-credits");
  const driverVisible = on("mobile-app");

  if (loaded && !citizenVisible && !driverVisible) {
    return (
      <Screen>
        <Notice
          tone="amber"
          title="H2Fleet mobile is disabled"
          body="All mobile-facing modules are turned off in the feature toggle service. They will reappear automatically when re-enabled."
        />
      </Screen>
    );
  }

  return (
    <Tab.Navigator
      screenOptions={({ route }) => ({
        headerShown: false,
        tabBarActiveTintColor: colors.accent,
        tabBarInactiveTintColor: colors.textFaint,
        tabBarIcon: ({ color, size }) => (
          <Ionicons name={TAB_ICONS[route.name] ?? "ellipse-outline"} color={color} size={size} />
        ),
      })}
    >
      {on("passenger-pwa") ? <Tab.Screen name="Arrivals" component={ArrivalsScreen} /> : null}
      {on("demand-responsive") ? <Tab.Screen name="DRT" component={DrtScreen} /> : null}
      {on("passenger-pwa") ? <Tab.Screen name="Alerts" component={AlertsScreen} /> : null}
      {on("carbon-credits") ? <Tab.Screen name="Carbon" component={CarbonScreen} /> : null}
      {driverVisible ? <Tab.Screen name="Driver" component={DriverScreen} /> : null}
    </Tab.Navigator>
  );
}

export default function App() {
  // First-run onboarding (persona select + account request). In-memory for
  // now — persistence lands with the auth iteration.
  const [showOnboarding, setShowOnboarding] = React.useState(true);

  return (
    <SafeAreaProvider>
      <QueryClientProvider client={queryClient}>
        <NavigationContainer theme={navTheme}>
          <StatusBar style="dark" />
          {showOnboarding ? (
            <OnboardingScreen onDone={() => setShowOnboarding(false)} />
          ) : (
            <Tabs />
          )}
        </NavigationContainer>
      </QueryClientProvider>
    </SafeAreaProvider>
  );
}

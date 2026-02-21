import * as countries from "i18n-iso-countries";
import zhLocale from "i18n-iso-countries/langs/zh.json";

countries.registerLocale(zhLocale);

export interface RegionOption {
    code: string;
    name: string;
}

export const getAllRegions = (): RegionOption[] => {
    const names = countries.getNames("zh", { select: "official" });
    return Object.entries(names).map(([code, name]) => ({
        code,
        name: `${code} (${name})`,
    })).sort((a, b) => a.code.localeCompare(b.code));
};

export const getRegionName = (code: string): string | undefined => {
    return countries.getName(code, "zh", { select: "official" });
};

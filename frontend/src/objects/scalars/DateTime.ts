declare const dateTimeBrand: unique symbol;

/**
 * A date and time, represented as a number of milliseconds since the Unix epoch.
 */
export type DateTime = number & { readonly [dateTimeBrand]: "DateTimeScalar" };

export const DateTime = {
  fromDate: (date: Date): DateTime => date.getTime() as DateTime,
};
